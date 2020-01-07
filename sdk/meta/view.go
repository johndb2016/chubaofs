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

package meta

import (
	"encoding/json"
	"github.com/chubaofs/chubaofs/proto"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/chubaofs/chubaofs/third_party/juju/errors"

	"github.com/chubaofs/chubaofs/util/log"
)

const (
	MaxSendToMaster = 3
)

var (
	NotLeader = errors.New("NotLeader")
)

type VolumeView struct {
	VolName        string
	MetaPartitions []*MetaPartition
}

type ClusterInfo struct {
	Cluster string
	Ip      string
}

type VolStatInfo struct {
	Name      string
	TotalSize uint64
	UsedSize  uint64
}

// VolName view managements
//
func (mw *MetaWrapper) PullVolumeView() (*VolumeView, error) {
	params := make(map[string]string)
	params["name"] = mw.volname
	body, err := mw.master.Request(http.MethodPost, MetaPartitionViewURL, params, nil)
	if err != nil {
		log.LogWarnf("PullVolumeView request: err(%v)", err)
		return nil, err
	}

	view := new(VolumeView)
	if err = json.Unmarshal(body, view); err != nil {
		log.LogWarnf("PullVolumeView unmarshal: err(%v) body(%v)", err, string(body))
		return nil, err
	}
	return view, nil
}

func (mw *MetaWrapper) UpdateClusterInfo() error {
	body, err := mw.master.Request(http.MethodPost, GetClusterInfoURL, nil, nil)
	if err != nil {
		log.LogWarnf("UpdateClusterInfo request: err(%v)", err)
		return err
	}

	info := new(ClusterInfo)
	if err = json.Unmarshal(body, info); err != nil {
		log.LogWarnf("UpdateClusterInfo unmarshal: err(%v)", err)
		return err
	}
	log.LogInfof("ClusterInfo: %v", *info)
	mw.cluster = info.Cluster
	mw.localIP = info.Ip
	return nil
}

func (mw *MetaWrapper) QueryTokenType(volume, token string) (int8, error) {
	params := make(map[string]string)
	params["name"] = volume
	params["token"] = url.QueryEscape(token)
	body, err := mw.master.Request(http.MethodPost, TokenGetURI, params, nil)
	if err != nil {
		log.LogWarnf("QueryTokenType request: err(%v)", err)
		return proto.ReadOnlyToken, err
	}

	protoToken := new(proto.Token)
	if err = json.Unmarshal(body, protoToken); err != nil {
		log.LogWarnf("QueryTokenType unmarshal: err(%v)", err)
		return proto.ReadOnlyToken, err
	}

	log.LogInfof("token: %v", protoToken)
	return protoToken.TokenType, nil
}

func (mw *MetaWrapper) UpdateVolStatInfo() error {
	params := make(map[string]string)
	params["name"] = mw.volname
	body, err := mw.master.Request(http.MethodPost, GetVolStatURL, params, nil)
	if err != nil {
		log.LogWarnf("UpdateVolStatInfo request: err(%v)", err)
		return err
	}

	info := new(VolStatInfo)
	if err = json.Unmarshal(body, info); err != nil {
		log.LogWarnf("UpdateVolStatInfo unmarshal: err(%v)", err)
		return err
	}
	log.LogInfof("UpdateVolStatInfo: info(%v)", *info)
	atomic.StoreUint64(&mw.totalSize, info.TotalSize)
	atomic.StoreUint64(&mw.usedSize, info.UsedSize)
	return nil
}

func (mw *MetaWrapper) UpdateMetaPartitions() error {
	nv, err := mw.PullVolumeView()
	if err != nil {
		return err
	}

	for _, mp := range nv.MetaPartitions {
		mw.replaceOrInsertPartition(mp)
		log.LogInfof("UpdateMetaPartition: mp(%v)", mp)
	}
	return nil
}

func (mw *MetaWrapper) refresh() {
	t := time.NewTicker(RefreshMetaPartitionsInterval)
	for {
		select {
		case <-t.C:
			mw.UpdateMetaPartitions()
			mw.UpdateVolStatInfo()
		}
	}
}
