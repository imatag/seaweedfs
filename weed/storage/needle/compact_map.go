package needle

import (
	. "github.com/chrislusf/seaweedfs/weed/storage/types"
	"github.com/chrislusf/seaweedfs/weed/glog"
	"sync"
	"sort"
	"runtime"
)

type CompactSection struct {
	sync.RWMutex
	values   []NeedleValue
	overflow map[NeedleId]NeedleValue
	start    NeedleId
	end      NeedleId
	counter  int
}

func NewCompactSection(start NeedleId) *CompactSection {
	return &CompactSection{
		values:   make([]NeedleValue, batch),
		overflow: make(map[NeedleId]NeedleValue),
		start:    start,
	}
}

//return old entry size
func (cs *CompactSection) Set(key NeedleId, offset Offset, size uint32) (oldOffset Offset, oldSize uint32) {
	cs.Lock()
	if key > cs.end {
		cs.end = key
	}
	if i := cs.binarySearchValues(key); i >= 0 {
		oldOffset, oldSize = cs.values[i].Offset, cs.values[i].Size
		//println("key", key, "old size", ret)
		cs.values[i].Offset, cs.values[i].Size = offset, size
	} else {
		needOverflow := cs.counter >= batch
		needOverflow = needOverflow || cs.counter > 0 && cs.values[cs.counter-1].Key > key
		if needOverflow {
			//println("start", cs.start, "counter", cs.counter, "key", key)
			if oldValue, found := cs.overflow[key]; found {
				oldOffset, oldSize = oldValue.Offset, oldValue.Size
			}
			cs.overflow[key] = NeedleValue{Key: key, Offset: offset, Size: size}
		} else {
			p := &cs.values[cs.counter]
			p.Key, p.Offset, p.Size = key, offset, size
			//println("added index", cs.counter, "key", key, cs.values[cs.counter].Key)
			cs.counter++
		}
	}
	cs.Unlock()
	return
}

//return old entry size
func (cs *CompactSection) Delete(key NeedleId) uint32 {
	cs.Lock()
	ret := uint32(0)
	if i := cs.binarySearchValues(key); i >= 0 {
		if cs.values[i].Size > 0 {
			ret = cs.values[i].Size
			cs.values[i].Size = 0
		}
	}
	if v, found := cs.overflow[key]; found {
		delete(cs.overflow, key)
		ret = v.Size
	}
	cs.Unlock()
	return ret
}
func (cs *CompactSection) Get(key NeedleId) (*NeedleValue, bool) {
	cs.RLock()
	if v, ok := cs.overflow[key]; ok {
		cs.RUnlock()
		return &v, true
	}
	if i := cs.binarySearchValues(key); i >= 0 {
		cs.RUnlock()
		return &cs.values[i], true
	}
	cs.RUnlock()
	return nil, false
}
func (cs *CompactSection) binarySearchValues(key NeedleId) int {
	l, h := 0, cs.counter-1
	if h >= 0 && cs.values[h].Key < key {
		return -2
	}
	//println("looking for key", key)
	for l <= h {
		m := (l + h) / 2
		//println("mid", m, "key", cs.values[m].Key, cs.values[m].Offset, cs.values[m].Size)
		if cs.values[m].Key < key {
			l = m + 1
		} else if key < cs.values[m].Key {
			h = m - 1
		} else {
			//println("found", m)
			return m
		}
	}
	return -1
}

//This map assumes mostly inserting increasing keys
//This map assumes mostly inserting increasing keys
type CompactMap struct {
	list []*CompactSection
}

func NewCompactMap() *CompactMap {
	return &CompactMap{}
}

func (cm *CompactMap) Set(key NeedleId, offset Offset, size uint32) (oldOffset Offset, oldSize uint32) {
	x := cm.binarySearchCompactSection(key)
	if x < 0 {
		//println(x, "creating", len(cm.list), "section, starting", key)
		cm.list = append(cm.list, NewCompactSection(key))
		x = len(cm.list) - 1
		//keep compact section sorted by start
		for x > 0 {
			if cm.list[x-1].start > cm.list[x].start {
				cm.list[x-1], cm.list[x] = cm.list[x], cm.list[x-1]
				x = x - 1
			} else {
				break
			}
		}
	}
	return cm.list[x].Set(key, offset, size)
}
func (cm *CompactMap) Delete(key NeedleId) uint32 {
	x := cm.binarySearchCompactSection(key)
	if x < 0 {
		return uint32(0)
	}
	return cm.list[x].Delete(key)
}
func (cm *CompactMap) Get(key NeedleId) (*NeedleValue, bool) {
	x := cm.binarySearchCompactSection(key)
	if x < 0 {
		return nil, false
	}
	return cm.list[x].Get(key)
}
func (cm *CompactMap) binarySearchCompactSection(key NeedleId) int {
	l, h := 0, len(cm.list)-1
	if h < 0 {
		return -5
	}
	if cm.list[h].start <= key {
		if cm.list[h].counter < batch || key <= cm.list[h].end {
			return h
		}
		return -4
	}
	for l <= h {
		m := (l + h) / 2
		if key < cm.list[m].start {
			h = m - 1
		} else { // cm.list[m].start <= key
			if cm.list[m+1].start <= key {
				l = m + 1
			} else {
				return m
			}
		}
	}
	return -3
}

// Visit visits all entries or stop if any error when visiting
func (cm *CompactMap) Visit(visit func(NeedleValue) error) error {
	for _, cs := range cm.list {
		cs.RLock()
		for _, v := range cs.overflow {
			if err := visit(v); err != nil {
				cs.RUnlock()
				return err
			}
		}
		for _, v := range cs.values {
			if _, found := cs.overflow[v.Key]; !found {
				if err := visit(v); err != nil {
					cs.RUnlock()
					return err
				}
			}
		}
		cs.RUnlock()
	}
	return nil
}

// Optimize packs the data structure to minimize memory usage
// useful after loading a messed up idx file
// it may temporarily allocate quite a bit though
func (cm *CompactMap) Optimize() {
	glog.V(1).Infof("optimize ", len(cm.list), cap(cm.list))
	
	// call Visit to read all keys and store them in a temporary array for sorting
	var delta []NeedleValue
	cm.Visit(func(needleValue NeedleValue) error {
		delta = append(delta, needleValue)
		return nil
	})
	
	// sort by Key
	sort.Slice(delta, func(i, j int) bool {
		return delta[i].Key < delta[j].Key
	})

	// clear everything and add back
	cm.list = nil

	for _, needleValue := range delta {
		cm.Set(needleValue.Key, needleValue.Offset, needleValue.Size)
	}
	
	runtime.GC()
	
	glog.V(1).Infof("repacked", len(cm.list), cap(cm.list))
}
