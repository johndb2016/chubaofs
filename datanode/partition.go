// Copyright 2018 The Containerfs Authors.
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

package datanode

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/juju/errors"
	"github.com/tiglabs/containerfs/master"
	"github.com/tiglabs/containerfs/proto"
	"github.com/tiglabs/containerfs/storage"
	"github.com/tiglabs/containerfs/util/log"
)

const (
	DataPartitionPrefix       = "datapartition"
	DataPartitionMetaFileName = "META"
	TimeLayout                = "2006-01-02 15:04:05"
)

var (
	AdminGetDataPartition     = master.AdminGetDataPartition
	ErrNotLeader              = errors.New("not leader")
	LeastGoalNum              = 2
	ErrLackOfGoal             = errors.New("dataPartitionGoal is not equal dataPartitionHosts")
	ErrDataPartitionOnBadDisk = errors.New("error bad disk")
)

type DataPartition interface {
	ID() uint32
	Path() string
	IsLeader() bool
	ReplicaHosts() []string
	Disk() *Disk

	Size() int
	Used() int
	Available() int

	Status() int
	ChangeStatus(status int)

	GetExtentStore() *storage.ExtentStore
	GetExtentCount() int
	GetAllWaterMarker() (files []*storage.FileInfo, err error)

	LaunchRepair()
	MergeRepair(metas *MembersFileMetas)

	FlushDelete() error

	AddWriteMetrics(latency uint64)
	AddReadMetrics(latency uint64)

	Stop()
}

type dataPartitionMeta struct {
	VolumeId      string
	PartitionType string
	PartitionId   uint32
	PartitionSize int
	CreateTime    string
}

func (meta *dataPartitionMeta) Validate() (err error) {
	meta.VolumeId = strings.TrimSpace(meta.VolumeId)
	meta.PartitionType = strings.TrimSpace(meta.PartitionType)
	if len(meta.VolumeId) == 0 || len(meta.PartitionType) == 0 ||
		meta.PartitionId == 0 || meta.PartitionSize == 0 {
		err = errors.New("illegal data partition meta")
		return
	}
	return
}

type dataPartition struct {
	volumeId        string
	partitionId     uint32
	partitionStatus int
	partitionSize   int
	replicaHosts    []string
	disk            *Disk
	isLeader        bool
	path            string
	used            int
	extentStore     *storage.ExtentStore
	stopC           chan bool

	runtimeMetrics          *DataPartitionMetrics
	updateReplicationTime   int64
	updatePartitionSizeTime int64
}

func CreateDataPartition(volId string, partitionId uint32, disk *Disk, size int, partitionType string) (dp DataPartition, err error) {

	if dp, err = newDataPartition(volId, partitionId, disk, size); err != nil {
		return
	}
	// Store meta information into meta file.
	var (
		metaFile *os.File
		metaData []byte
	)
	metaFilePath := path.Join(dp.Path(), DataPartitionMetaFileName)
	if metaFile, err = os.OpenFile(metaFilePath, os.O_CREATE|os.O_RDWR, 0666); err != nil {
		return
	}
	defer metaFile.Close()
	meta := &dataPartitionMeta{
		VolumeId:      volId,
		PartitionId:   partitionId,
		PartitionType: partitionType,
		PartitionSize: size,
		CreateTime:    time.Now().Format(TimeLayout),
	}
	if metaData, err = json.Marshal(meta); err != nil {
		return
	}
	if _, err = metaFile.Write(metaData); err != nil {
		return
	}
	return
}

// LoadDataPartition load and returns partition instance from specified directory.
// This method will read the partition meta file stored under the specified directory
// and create partition instance.
func LoadDataPartition(partitionDir string, disk *Disk) (dp DataPartition, err error) {
	var (
		metaFileData []byte
	)
	if metaFileData, err = ioutil.ReadFile(path.Join(partitionDir, DataPartitionMetaFileName)); err != nil {
		return
	}
	meta := &dataPartitionMeta{}
	if err = json.Unmarshal(metaFileData, meta); err != nil {
		return
	}
	if err = meta.Validate(); err != nil {
		return
	}
	dp, err = newDataPartition(meta.VolumeId, meta.PartitionId, disk, meta.PartitionSize)
	return
}

func newDataPartition(volumeId string, partitionId uint32, disk *Disk, size int) (dp DataPartition, err error) {
	partition := &dataPartition{
		volumeId:        volumeId,
		partitionId:     partitionId,
		disk:            disk,
		path:            path.Join(disk.Path, fmt.Sprintf(DataPartitionPrefix+"_%v_%v", partitionId, size)),
		partitionSize:   size,
		replicaHosts:    make([]string, 0),
		stopC:           make(chan bool, 0),
		partitionStatus: proto.ReadWrite,
		runtimeMetrics:  NewDataPartitionMetrics(),
	}
	partition.extentStore, err = storage.NewExtentStore(partition.path, size)
	if err != nil {
		return
	}
	disk.AttachDataPartition(partition)
	dp = partition
	go partition.statusUpdateScheduler()
	return
}

func (dp *dataPartition) ID() uint32 {
	return dp.partitionId
}

func (dp *dataPartition) GetExtentCount() int {
	return dp.extentStore.GetExtentCount()
}

func (dp *dataPartition) Path() string {
	return dp.path
}

func (dp *dataPartition) IsLeader() bool {
	return dp.isLeader
}

func (dp *dataPartition) ReplicaHosts() []string {
	return dp.replicaHosts
}

func (dp *dataPartition) Stop() {
	if dp.stopC != nil {
		close(dp.stopC)
	}
	// Close all store and backup partition data file.
	dp.extentStore.Close()

}

func (dp *dataPartition) FlushDelete() (err error) {
	err = dp.extentStore.FlushDelete()
	return
}

func (dp *dataPartition) Disk() *Disk {
	return dp.disk
}

func (dp *dataPartition) Status() int {
	return dp.partitionStatus
}

func (dp *dataPartition) Size() int {
	return dp.partitionSize
}

func (dp *dataPartition) Used() int {
	return dp.used
}

func (dp *dataPartition) Available() int {
	return dp.partitionSize - dp.used
}

func (dp *dataPartition) ChangeStatus(status int) {
	switch status {
	case proto.ReadOnly, proto.ReadWrite, proto.Unavaliable:
		dp.partitionStatus = status
	}
}

func (dp *dataPartition) statusUpdateScheduler() {
	ticker := time.NewTicker(10 * time.Second)
	metricTicker := time.NewTicker(2 * time.Second)
	cleanUpTicker := time.NewTicker(time.Second * 5)
	for {
		select {
		case <-ticker.C:
			dp.statusUpdate()
			dp.LaunchRepair()
		case <-cleanUpTicker.C:
			dp.extentStore.Cleanup()
		case <-dp.stopC:
			ticker.Stop()
			return
		case <-metricTicker.C:
			dp.runtimeMetrics.recomputLatency()
		}
	}
}

func (dp *dataPartition) statusUpdate() {
	status := proto.ReadWrite
	dp.computeUsage()
	if dp.used >= dp.partitionSize {
		status = proto.ReadOnly
	}
	if dp.extentStore.GetExtentCount() >= MaxActiveExtents {
		status = proto.ReadOnly
	}
	dp.partitionStatus = int(math.Min(float64(status), float64(dp.disk.Status)))
}

func (dp *dataPartition) computeUsage() {
	var (
		used  int64
		files []os.FileInfo
		err   error
	)
	if time.Now().Unix()-dp.updatePartitionSizeTime < UpdatePartitionSizeTime {
		return
	}
	if files, err = ioutil.ReadDir(dp.path); err != nil {
		return
	}
	for _, file := range files {
		used += file.Size()
	}
	dp.used = int(used)
	dp.updatePartitionSizeTime = time.Now().Unix()
}

func (dp *dataPartition) GetExtentStore() *storage.ExtentStore {
	return dp.extentStore
}

func (dp *dataPartition) String() (m string) {
	return fmt.Sprintf(DataPartitionPrefix+"_%v_%v", dp.partitionId, dp.partitionSize)
}

func (dp *dataPartition) LaunchRepair() {
	if dp.partitionStatus == proto.Unavaliable {
		return
	}
	select {
	case <-dp.stopC:
		return
	default:
	}
	if err := dp.updateReplicaHosts(); err != nil {
		log.LogErrorf("action[LaunchRepair] err[%v].", err)
		return
	}
	if !dp.isLeader {
		return
	}
	dp.fileRepair()
}

func (dp *dataPartition) updateReplicaHosts() (err error) {
	if time.Now().Unix()-dp.updateReplicationTime <= UpdateReplicationHostsTime {
		return
	}
	dp.isLeader = false
	isLeader, replicas, err := dp.fetchReplicaHosts()
	if err != nil {
		return
	}
	if !dp.compareReplicaHosts(dp.replicaHosts, replicas) {
		log.LogInfof(fmt.Sprintf("action[updateReplicaHosts] partition[%v] replicaHosts changed from [%v] to [%v].",
			dp.partitionId, dp.replicaHosts, replicas))
	}
	dp.isLeader = isLeader
	dp.replicaHosts = replicas
	dp.updateReplicationTime = time.Now().Unix()
	log.LogInfof(fmt.Sprintf("ActionUpdateReplicationHosts partiton[%v]", dp.partitionId))
	return
}

func (dp *dataPartition) compareReplicaHosts(v1, v2 []string) (equals bool) {
	// Compare fetched replica hosts with local stored hosts.
	equals = true
	if len(v1) == len(v2) {
		for i := 0; i < len(v1); i++ {
			if v1[i] != v2[i] {
				equals = false
				return
			}
		}
		equals = true
		return
	}
	equals = false
	return
}

func (dp *dataPartition) fetchReplicaHosts() (isLeader bool, replicaHosts []string, err error) {
	var (
		HostsBuf []byte
	)
	params := make(map[string]string)
	params["id"] = strconv.Itoa(int(dp.partitionId))
	if HostsBuf, err = MasterHelper.Request("GET", AdminGetDataPartition, params, nil); err != nil {
		isLeader = false
		return
	}
	response := &master.DataPartition{}
	replicaHosts = make([]string, 0)
	if err = json.Unmarshal(HostsBuf, &response); err != nil {
		isLeader = false
		replicaHosts = nil
		return
	}
	for _, host := range response.PersistenceHosts {
		replicaHosts = append(replicaHosts, host)
	}
	if response.PersistenceHosts != nil && len(response.PersistenceHosts) >= 1 {
		leaderAddr := response.PersistenceHosts[0]
		leaderAddrParts := strings.Split(leaderAddr, ":")
		if len(leaderAddrParts) == 2 && strings.TrimSpace(leaderAddrParts[0]) == LocalIP {
			isLeader = true
		}
	}
	return
}

func (dp *dataPartition) Load() (response *proto.LoadDataPartitionResponse) {
	response = &proto.LoadDataPartitionResponse{}
	response.PartitionId = uint64(dp.partitionId)
	response.PartitionStatus = dp.partitionStatus
	response.Used = uint64(dp.Used())
	var err error
	response.PartitionSnapshot, err = dp.extentStore.SnapShot()
	if err != nil {
		response.Status = proto.TaskFail
		response.Result = err.Error()
		return
	}
	return
}

func (dp *dataPartition) GetAllWaterMarker() (files []*storage.FileInfo, err error) {
	files, err = dp.extentStore.GetAllWatermark(storage.GetStableExtentFilter())
	if err != nil {
		return nil, err
	}
	return
}

func (dp *dataPartition) MergeRepair(metas *MembersFileMetas) {
	store := dp.extentStore
	for _, deleteExtentId := range metas.NeedDeleteExtentsTasks {
		if deleteExtentId.FileId <= storage.TinyChunkCount {
			continue
		}
		store.MarkDelete(uint64(deleteExtentId.FileId))
	}
	for _, addExtent := range metas.NeedAddExtentsTasks {
		if addExtent.FileId <= storage.TinyChunkCount {
			continue
		}
		if store.IsExistExtent(uint64(addExtent.FileId)) {
			fixFileSizeTask := &storage.FileInfo{Source: addExtent.Source, FileId: addExtent.FileId, Size: addExtent.Size}
			metas.NeedFixFileSizeTasks = append(metas.NeedFixFileSizeTasks, fixFileSizeTask)
			continue
		}
		err := store.Create(uint64(addExtent.FileId), addExtent.Inode, false)
		if err != nil {
			continue
		}
		fixFileSizeTask := &storage.FileInfo{Source: addExtent.Source, FileId: addExtent.FileId, Size: addExtent.Size}
		metas.NeedFixFileSizeTasks = append(metas.NeedFixFileSizeTasks, fixFileSizeTask)
	}

	var (
		wg           *sync.WaitGroup
		recoverIndex int
	)
	wg = new(sync.WaitGroup)
	for _, fixExtent := range metas.NeedFixFileSizeTasks {
		if fixExtent.FileId <= storage.TinyChunkCount {
			continue
		}
		if !store.IsExistExtent(uint64(fixExtent.FileId)) {
			continue
		}
		wg.Add(1)
		go dp.doStreamExtentFixRepair(wg, fixExtent)
		recoverIndex++
		if recoverIndex%SimultaneouslyRecoverFiles == 0 {
			wg.Wait()
		}
	}
	wg.Wait()
}

func (dp *dataPartition) AddWriteMetrics(latency uint64) {
	dp.runtimeMetrics.AddWriteMetrics(latency)
}

func (dp *dataPartition) AddReadMetrics(latency uint64) {
	dp.runtimeMetrics.AddReadMetrics(latency)
}
