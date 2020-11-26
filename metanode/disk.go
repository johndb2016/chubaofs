// Copyright 2018 The Chubao Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package metanode

import (
	"sync"
	"syscall"
	"time"

	"github.com/chubaofs/chubaofs/util/log"
)

// Disk represents the structure of the disk
type Disk struct {
	sync.RWMutex
	Path      string
	Total     float64
	Used      float64
	Available float64

	stopCh chan struct{}
}

func NewDisk(path string) (d *Disk) {
	d = new(Disk)
	d.Path = path
	d.stopCh = make(chan struct{}, 1)
	d.computeUsage()
	d.startScheduleToUpdateSpaceInfo()
	return
}

// Compute the disk usage
func (d *Disk) computeUsage() (err error) {
	d.RLock()
	defer d.RUnlock()
	fs := syscall.Statfs_t{}
	err = syscall.Statfs(d.Path, &fs)
	if err != nil {
		return
	}

	d.Total = float64(fs.Blocks) * float64(fs.Bsize)
	d.Available = float64(fs.Bavail) * float64(fs.Bsize)
	d.Used = d.Total - d.Available

	log.LogDebugf("action[computeUsage] disk(%v) all(%v) available(%v) used(%v)", d.Path, d.Total, d.Available, d.Used)

	return
}

func (d *Disk) startScheduleToUpdateSpaceInfo() {
	go func() {
		updateSpaceInfoTicker := time.NewTicker(10 * time.Second)
		defer func() {
			updateSpaceInfoTicker.Stop()
		}()
		for {
			select {
			case <-d.stopCh:
				log.LogInfof("[MetaNode]stop disk: %v stat  \n", d.Path)
				break
			case <-updateSpaceInfoTicker.C:
				d.computeUsage()
			}
		}
	}()
}

func (d *Disk) stopScheduleToUpdateSpaceInfo() {
	d.stopCh <- struct{}{}
}

func (m *MetaNode) startDiskStat() error {
	m.disks = make(map[string]*Disk)
	m.disks[m.metadataDir] = NewDisk(m.metadataDir)
	m.disks[m.raftDir] = NewDisk(m.raftDir)
	return nil
}

func (m *MetaNode) stopDiskStat() {
	for _, d := range m.disks {
		d.stopScheduleToUpdateSpaceInfo()
	}
}

func (m *MetaNode) getDiskStat() []*Disk {
	ds := make([]*Disk, 0)
	for _, d := range m.disks {
		ds = append(ds, d)
	}

	return ds
}
