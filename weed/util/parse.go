package util

import (
	"strconv"
	"path/filepath"
	"strings"
)

func ParseInt(text string, defaultValue int) int {
	count, parseError := strconv.ParseInt(text, 10, 64)
	if parseError != nil {
		if len(text) > 0 {
			return 0
		}
		return defaultValue
	}
	return int(count)
}
func ParseUint64(text string, defaultValue uint64) uint64 {
	count, parseError := strconv.ParseUint(text, 10, 64)
	if parseError != nil {
		if len(text) > 0 {
			return 0
		}
		return defaultValue
	}
	return count
}

func ParseURLPath(path string) (vid, fid, filename, ext string, isVolumeIdOnly bool) {
	switch strings.Count(path, "/") {
	case 3:
		parts := strings.Split(path, "/")
		vid, fid, filename = parts[1], parts[2], parts[3]
		ext = filepath.Ext(filename)
	case 2:
		parts := strings.Split(path, "/")
		vid, fid = parts[1], parts[2]
		dotIndex := strings.LastIndex(fid, ".")
		if dotIndex > 0 {
			ext = fid[dotIndex:]
			fid = fid[0:dotIndex]
		}
	default:
		sepIndex := strings.LastIndex(path, "/")
		commaIndex := strings.LastIndex(path[sepIndex:], ",")
		if commaIndex <= 0 {
			vid, isVolumeIdOnly = path[sepIndex+1:], true
			return
		}
		dotIndex := strings.LastIndex(path[sepIndex:], ".")
		vid = path[sepIndex+1 : commaIndex]
		fid = path[commaIndex+1:]
		ext = ""
		if dotIndex > 0 {
			fid = path[commaIndex+1 : dotIndex]
			ext = path[dotIndex:]
		}
	}
	return
}
