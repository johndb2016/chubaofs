// Copyright 2018 The CFS Authors.
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

package master

import (
	"encoding/json"
	"fmt"
	"github.com/chubaofs/chubaofs/third_party/juju/errors"
	"github.com/chubaofs/chubaofs/util"
	"github.com/chubaofs/chubaofs/util/log"
	"net/http"
	"regexp"
	"strconv"
)

type VolStatInfo struct {
	Name      string
	TotalSize uint64
	UsedSize  uint64
}

type DataPartitionResponse struct {
	PartitionID   uint64
	Status        int8
	ReplicaNum    uint8
	PartitionType string
	Hosts         []string
}

type DataPartitionsView struct {
	DataPartitions []*DataPartitionResponse
}

func NewDataPartitionsView() (dataPartitionsView *DataPartitionsView) {
	dataPartitionsView = new(DataPartitionsView)
	dataPartitionsView.DataPartitions = make([]*DataPartitionResponse, 0)
	return
}

type MetaPartitionView struct {
	PartitionID uint64
	Start       uint64
	End         uint64
	Members     []string
	LeaderAddr  string
	Status      int8
}

type VolView struct {
	Name           string
	VolType        string
	Status         uint8
	MetaPartitions []*MetaPartitionView
	DataPartitions []*DataPartitionResponse
}

func NewVolView(name, volType string, status uint8) (view *VolView) {
	view = new(VolView)
	view.Name = name
	view.VolType = volType
	view.Status = status
	view.MetaPartitions = make([]*MetaPartitionView, 0)
	view.DataPartitions = make([]*DataPartitionResponse, 0)
	return
}

func NewMetaPartitionView(partitionID, start, end uint64, status int8) (mpView *MetaPartitionView) {
	mpView = new(MetaPartitionView)
	mpView.PartitionID = partitionID
	mpView.Start = start
	mpView.End = end
	mpView.Status = status
	mpView.Members = make([]string, 0)
	return
}

func (m *Master) getDataPartitions(w http.ResponseWriter, r *http.Request) {
	var (
		body []byte
		code = http.StatusBadRequest
		name string
		vol  *Vol
		err  error
	)
	if name, err = parseGetVolPara(r); err != nil {
		goto errDeal
	}
	if vol, err = m.cluster.getVol(name); err != nil {
		err = errors.Annotatef(VolNotFound, "%v not found", name)
		code = http.StatusNotFound
		goto errDeal
	}

	if body, err = vol.getDataPartitionsView(m.cluster.getLiveDataNodesRate()); err != nil {
		goto errDeal
	}
	w.Write(body)
	return
errDeal:
	logMsg := getReturnMessage("getDataPartitions", r.RemoteAddr, err.Error(), code)
	HandleError(logMsg, err, code, w)
	return
}

func (m *Master) getVol(w http.ResponseWriter, r *http.Request) {
	var (
		body []byte
		code = http.StatusBadRequest
		err  error
		name string
		vol  *Vol
		view *VolView
	)
	if name, err = parseGetVolPara(r); err != nil {
		goto errDeal
	}
	if vol, err = m.cluster.getVol(name); err != nil {
		err = errors.Annotatef(VolNotFound, "%v not found", name)
		code = http.StatusNotFound
		goto errDeal
	}
	if view, err = m.getVolView(vol); err != nil {
		goto errDeal
	}
	if body, err = json.Marshal(view); err != nil {
		goto errDeal
	}
	w.Write(body)
	return
errDeal:
	logMsg := getReturnMessage("getVol", r.RemoteAddr, err.Error(), code)
	HandleError(logMsg, err, code, w)
	return
}

func (m *Master) getVolStatInfo(w http.ResponseWriter, r *http.Request) {
	var (
		body []byte
		code = http.StatusBadRequest
		err  error
		name string
		vol  *Vol
	)
	if name, err = parseGetVolPara(r); err != nil {
		goto errDeal
	}
	if vol, err = m.cluster.getVol(name); err != nil {
		err = errors.Annotatef(VolNotFound, "%v not found", name)
		code = http.StatusNotFound
		goto errDeal
	}
	if body, err = json.Marshal(volStat(vol)); err != nil {
		goto errDeal
	}
	w.Write(body)
	return
errDeal:
	logMsg := getReturnMessage("getVolStatInfo", r.RemoteAddr, err.Error(), code)
	HandleError(logMsg, err, code, w)
	return
}

func (m *Master) getVolView(vol *Vol) (view *VolView, err error) {
	view = NewVolView(vol.Name, vol.VolType, vol.Status)
	setMetaPartitions(vol, view, m.cluster.getLiveMetaNodesRate())
	err = setDataPartitions(vol, view, m.cluster.getLiveDataNodesRate())
	return
}
func setDataPartitions(vol *Vol, view *VolView, liveRate float32) (err error) {
	if liveRate < NodesAliveRate {
		return
	}
	lessThan := vol.getTotalUsedSpace() < (vol.Capacity * util.GB)
	vol.dataPartitions.RLock()
	defer vol.dataPartitions.RUnlock()
	//var minRWDpCount float64
	//minRWDpCount = float64(vol.dataPartitions.dataPartitionCount) * float64(VolReadWriteDataPartitionRatio)
	//lessThanRwCount := vol.dataPartitions.readWriteDataPartitions < int(minRWDpCount)
	if vol.dataPartitions.readWriteDataPartitions == 0 && lessThan {
		err = fmt.Errorf("action[setDataPartitions],vol[%v] no writeable data partitions", vol.Name)
		log.LogWarn(err.Error())
	} else {
		dpResps := vol.dataPartitions.GetDataPartitionsView(0)
		view.DataPartitions = dpResps
	}
	return
}
func setMetaPartitions(vol *Vol, view *VolView, liveRate float32) {
	if liveRate < NodesAliveRate {
		return
	}
	vol.mpsLock.RLock()
	defer vol.mpsLock.RUnlock()
	for _, mp := range vol.MetaPartitions {
		view.MetaPartitions = append(view.MetaPartitions, getMetaPartitionView(mp))
	}
}

func volStat(vol *Vol) (stat *VolStatInfo) {
	stat = new(VolStatInfo)
	stat.Name = vol.Name
	stat.TotalSize = vol.Capacity * util.GB
	stat.UsedSize = vol.getTotalUsedSpace()
	if stat.UsedSize > stat.TotalSize {
		stat.UsedSize = stat.TotalSize
	}
	log.LogDebugf("total[%v],usedSize[%v]", stat.TotalSize, stat.UsedSize)
	return
}

func getMetaPartitionView(mp *MetaPartition) (mpView *MetaPartitionView) {
	mpView = NewMetaPartitionView(mp.PartitionID, mp.Start, mp.End, mp.Status)
	mp.Lock()
	defer mp.Unlock()
	for _, host := range mp.PersistenceHosts {
		mpView.Members = append(mpView.Members, host)
	}
	mr, err := mp.getLeaderMetaReplica()
	if err != nil {
		return
	}
	mpView.LeaderAddr = mr.Addr
	return
}

func (m *Master) getMetaPartition(w http.ResponseWriter, r *http.Request) {
	var (
		body        []byte
		code        = http.StatusBadRequest
		err         error
		name        string
		partitionID uint64
		vol         *Vol
		mp          *MetaPartition
	)
	if name, partitionID, err = parseGetMetaPartitionPara(r); err != nil {
		goto errDeal
	}
	if vol, err = m.cluster.getVol(name); err != nil {
		err = errors.Annotatef(VolNotFound, "%v not found", name)
		code = http.StatusNotFound
		goto errDeal
	}
	if mp, err = vol.getMetaPartition(partitionID); err != nil {
		err = errors.Annotatef(MetaPartitionNotFound, "%v not found", partitionID)
		code = http.StatusNotFound
		goto errDeal
	}
	if body, err = mp.toJson(); err != nil {
		goto errDeal
	}
	w.Write(body)
	return
errDeal:
	logMsg := getReturnMessage("getMetaPartition", r.RemoteAddr, err.Error(), code)
	HandleError(logMsg, err, code, w)
	return
}

func parseGetMetaPartitionPara(r *http.Request) (name string, partitionID uint64, err error) {
	r.ParseForm()
	if name, err = checkVolPara(r); err != nil {
		return
	}
	if partitionID, err = checkMetaPartitionID(r); err != nil {
		return
	}
	return
}

func checkMetaPartitionID(r *http.Request) (partitionID uint64, err error) {
	var value string
	if value = r.FormValue(ParaId); value == "" {
		err = paraNotFound(ParaId)
		return
	}
	return strconv.ParseUint(value, 10, 64)
}

func parseGetVolPara(r *http.Request) (name string, err error) {
	r.ParseForm()
	return checkVolPara(r)
}

func checkAuthKeyPara(r *http.Request) (authKey string, err error) {
	if authKey = r.FormValue(ParaAuthKey); authKey == "" {
		err = paraNotFound(ParaAuthKey)
		return
	}
	return
}

func checkVolPara(r *http.Request) (name string, err error) {
	if name = r.FormValue(ParaName); name == "" {
		err = paraNotFound(ParaName)
		return
	}

	pattern := "^[a-zA-Z0-9_-]{3,256}$"
	reg, err := regexp.Compile(pattern)
	if err != nil {
		return "", err
	}

	if !reg.MatchString(name) {
		return "", errors.New("name can only be number and letters")
	}

	return
}
