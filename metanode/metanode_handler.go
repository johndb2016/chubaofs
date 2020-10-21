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

package metanode

import (
	"encoding/json"
	"net/http"
	"strconv"

	"strings"

	"github.com/chubaofs/chubaofs/proto"
)

func (m *MetaNode) registerHandler() (err error) {
	// Register http handler
	http.HandleFunc("/getAllPartitions", m.allPartitionsHandle)
	http.HandleFunc("/getPartitionById", m.getPartitionByIDHandler)
	http.HandleFunc("/getInodeRange", m.rangeHandle)
	http.HandleFunc("/getExtents", m.getExtents)
	http.HandleFunc("/getDentry", m.getDentryHandle)
	return
}
func (m *MetaNode) allPartitionsHandle(w http.ResponseWriter, r *http.Request) {
	mm := m.metaManager.(*metaManager)
	data, err := mm.PartitionsMarshalJSON()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	w.Write(data)
}

func (m *MetaNode) getPartitionByIDHandler(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	pid, err := strconv.ParseUint(r.FormValue("pid"), 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	mp, err := m.metaManager.GetPartition(pid)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(err.Error()))
		return
	}
	msg := make(map[string]interface{})
	leader, _ := mp.IsLeader()
	msg["leaderAddr"] = leader
	conf := mp.GetBaseConfig()
	msg["peers"] = conf.Peers
	msg["nodeId"] = conf.NodeId
	msg["cursor"] = conf.Cursor
	data, err := json.Marshal(msg)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(data)
}

func (m *MetaNode) rangeHandle(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, err := strconv.ParseUint(r.FormValue("id"), 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	mp, err := m.metaManager.GetPartition(id)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(err.Error()))
		return
	}
	mpp := mp.(*metaPartition)
	f := func(i BtreeItem) bool {
		var data []byte
		if data, err = json.Marshal(i); err != nil {
			return false
		}
		if _, err = w.Write(data); err != nil {
			return false
		}
		data[0] = byte('\n')
		if _, err = w.Write(data[:1]); err != nil {
			return false
		}
		return true
	}
	mpp.RangeInode(f)
}

func (m *MetaNode) getExtents(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	pidVal := r.FormValue("pid")
	idVal := r.FormValue("ino")
	if pidVal == "" || idVal == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	pid, err := strconv.ParseUint(pidVal, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	id, err := strconv.ParseUint(idVal, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(err.Error()))
		return
	}
	mp, err := m.metaManager.GetPartition(pid)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(err.Error()))
		return
	}
	mm := mp.(*metaPartition)
	resp := mm.getInode(NewInode(id, 0))
	if resp.Status != proto.OpOk {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("inode id not exist"))
		return
	}
	data, err := resp.Msg.Extents.Marshal()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Write(data)
	return
}

func (m *MetaNode) getDentryHandle(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	// get partition ID
	pidVal := r.FormValue("pid")
	if pidVal = strings.TrimSpace(pidVal); pidVal == "" {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("request param not pid"))
		return
	}
	pid, err := strconv.ParseUint(pidVal, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("not valid number"))
		return
	}
	p, err := m.metaManager.GetPartition(pid)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	mp := p.(*metaPartition)

	var val []byte
	mp.dentryTree.Ascend(func(i BtreeItem) bool {
		val, err = json.Marshal(i)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return false
		}
		if _, err = w.Write(val); err != nil {
			return false
		}
		val[0] = '\n'
		if _, err = w.Write(val[:1]); err != nil {
			return false
		}
		return true
	})
	return
}
