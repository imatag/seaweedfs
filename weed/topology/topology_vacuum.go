package topology

import (
	"encoding/json"
	"errors"
	"net/url"
	"math/rand"
	"time"

	"fmt"
	"github.com/chrislusf/seaweedfs/weed/glog"
	"github.com/chrislusf/seaweedfs/weed/storage"
	"github.com/chrislusf/seaweedfs/weed/util"
)

func batchVacuumVolumeCheck(vl *VolumeLayout, vid storage.VolumeId, locationlist *VolumeLocationList, garbageThreshold string) bool {
	ch := make(chan bool, locationlist.Length())
	for index, dn := range locationlist.list {
		go func(index int, url string, vid storage.VolumeId) {
			//glog.V(0).Infoln(index, "Check vacuuming", vid, "on", dn.Url())
			if e, ret := vacuumVolume_Check(url, vid, garbageThreshold); e != nil {
				//glog.V(0).Infoln(index, "Error when checking vacuuming", vid, "on", url, e)
				ch <- false
			} else {
				//glog.V(0).Infoln(index, "Checked vacuuming", vid, "on", url, "needVacuum", ret)
				ch <- ret
			}
		}(index, dn.Url(), vid)
	}
	isCheckSuccess := true
	for _ = range locationlist.list {
		select {
		case canVacuum := <-ch:
			isCheckSuccess = isCheckSuccess && canVacuum
		case <-time.After(30 * time.Minute):
			isCheckSuccess = false
			break
		}
	}
	return isCheckSuccess
}
func batchVacuumVolumeCompact(vl *VolumeLayout, vid storage.VolumeId, locationlist *VolumeLocationList, preallocate int64) bool {
	vl.removeFromWritable(vid)
	ch := make(chan bool, locationlist.Length())
	for index, dn := range locationlist.list {
		go func(index int, url string, vid storage.VolumeId) {
			glog.V(0).Infoln(index, "Start vacuuming", vid, "on", url)
			if e := vacuumVolume_Compact(url, vid, preallocate); e != nil {
				glog.V(0).Infoln(index, "Error when vacuuming", vid, "on", url, e)
				ch <- false
			} else {
				glog.V(0).Infoln(index, "Complete vacuuming", vid, "on", url)
				ch <- true
			}
		}(index, dn.Url(), vid)
	}
	isVacuumSuccess := true
	for _ = range locationlist.list {
		select {
		case canCommit := <-ch:
			isVacuumSuccess = isVacuumSuccess && canCommit
		case <-time.After(30 * time.Minute):
			isVacuumSuccess = false
			break
		}
	}
	return isVacuumSuccess
}
func batchVacuumVolumeCommit(vl *VolumeLayout, vid storage.VolumeId, locationlist *VolumeLocationList) bool {
	isCommitSuccess := true
	for _, dn := range locationlist.list {
		glog.V(0).Infoln("Start Commiting vacuum", vid, "on", dn.Url())
		if e := vacuumVolume_Commit(dn.Url(), vid); e != nil {
			glog.V(0).Infoln("Error when committing vacuum", vid, "on", dn.Url(), e)
			isCommitSuccess = false
		} else {
			glog.V(0).Infoln("Complete Commiting vacuum", vid, "on", dn.Url())
		}
		if isCommitSuccess {
			// set volume available, without locking
			vl.vid2location[vid].Set(dn)
			if vl.vid2location[vid].Length() >= vl.rp.GetCopyCount() {
				vl.setVolumeWritable(vid)
			}
		}
	}
	return isCommitSuccess
}
func batchVacuumVolumeCleanup(vl *VolumeLayout, vid storage.VolumeId, locationlist *VolumeLocationList) {
	for _, dn := range locationlist.list {
		glog.V(0).Infoln("Start cleaning up", vid, "on", dn.Url())
		if e := vacuumVolume_Cleanup(dn.Url(), vid); e != nil {
			glog.V(0).Infoln("Error when cleaning up", vid, "on", dn.Url(), e)
		} else {
			glog.V(0).Infoln("Complete cleaning up", vid, "on", dn.Url())
		}
	}
}

func shuffle(vals []storage.VolumeId) {
  r := rand.New(rand.NewSource(time.Now().Unix()))
  for len(vals) > 0 {
    n := len(vals)
    randIndex := r.Intn(n)
    vals[n-1], vals[randIndex] = vals[randIndex], vals[n-1]
    vals = vals[:n-1]
  }
}

func (t *Topology) Vacuum(garbageThreshold string, preallocate int64) int {
	glog.V(0).Infof("Start vacuum on demand with threshold:%s", garbageThreshold)
	for _, col := range t.collectionMap.Items() {
		c := col.(*Collection)
		for _, vl := range c.storageType2VolumeLayout.Items() {
			if vl != nil {
				volumeLayout := vl.(*VolumeLayout)
				volumeLayout.accessLock.RLock()

				// make a slice out of the vid keys
				keys := make([]storage.VolumeId, 0, len(volumeLayout.vid2location))
				for vid, _ := range volumeLayout.vid2location {
					keys = append(keys, vid)
				}

				// limit to at most 100 vacuums
				if len(keys) > 100 {
					shuffle(keys)
					keys = keys[:100]
				}

				for i := range keys {
					vid := keys[i]
					locationlist := volumeLayout.vid2location[vid]
					isReadOnly, hasValue := volumeLayout.readonlyVolumes[vid]

					if hasValue && isReadOnly {
						continue
					}

					glog.V(0).Infof("check vacuum on collection:%s volume:%d", c.Name, vid)
					if batchVacuumVolumeCheck(volumeLayout, vid, locationlist, garbageThreshold) {
						if batchVacuumVolumeCompact(volumeLayout, vid, locationlist, preallocate) {
							batchVacuumVolumeCommit(volumeLayout, vid, locationlist)
						}
					}
				}
				volumeLayout.accessLock.RUnlock()
			}
		}
	}
	return 0
}

type VacuumVolumeResult struct {
	Result bool
	Error  string
}

func vacuumVolume_Check(urlLocation string, vid storage.VolumeId, garbageThreshold string) (error, bool) {
	values := make(url.Values)
	values.Add("volume", vid.String())
	values.Add("garbageThreshold", garbageThreshold)
	jsonBlob, err := util.Post("http://"+urlLocation+"/admin/vacuum/check", values)
	if err != nil {
		glog.V(0).Infoln("parameters:", values)
		return err, false
	}
	var ret VacuumVolumeResult
	if err := json.Unmarshal(jsonBlob, &ret); err != nil {
		return err, false
	}
	if ret.Error != "" {
		return errors.New(ret.Error), false
	}
	return nil, ret.Result
}
func vacuumVolume_Compact(urlLocation string, vid storage.VolumeId, preallocate int64) error {
	values := make(url.Values)
	values.Add("volume", vid.String())
	values.Add("preallocate", fmt.Sprintf("%d", preallocate))
	jsonBlob, err := util.Post("http://"+urlLocation+"/admin/vacuum/compact", values)
	if err != nil {
		return err
	}
	var ret VacuumVolumeResult
	if err := json.Unmarshal(jsonBlob, &ret); err != nil {
		return err
	}
	if ret.Error != "" {
		return errors.New(ret.Error)
	}
	return nil
}
func vacuumVolume_Commit(urlLocation string, vid storage.VolumeId) error {
	values := make(url.Values)
	values.Add("volume", vid.String())
	jsonBlob, err := util.Post("http://"+urlLocation+"/admin/vacuum/commit", values)
	if err != nil {
		return err
	}
	var ret VacuumVolumeResult
	if err := json.Unmarshal(jsonBlob, &ret); err != nil {
		return err
	}
	if ret.Error != "" {
		return errors.New(ret.Error)
	}
	return nil
}
func vacuumVolume_Cleanup(urlLocation string, vid storage.VolumeId) error {
	values := make(url.Values)
	values.Add("volume", vid.String())
	jsonBlob, err := util.Post("http://"+urlLocation+"/admin/vacuum/cleanup", values)
	if err != nil {
		return err
	}
	var ret VacuumVolumeResult
	if err := json.Unmarshal(jsonBlob, &ret); err != nil {
		return err
	}
	if ret.Error != "" {
		return errors.New(ret.Error)
	}
	return nil
}
