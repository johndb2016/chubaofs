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
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/chubaofs/chubaofs/proto"
	"github.com/chubaofs/chubaofs/util"
	"github.com/chubaofs/chubaofs/util/log"
	"github.com/chubaofs/chubaofs/util/ump"
	"strings"
)

func newCreateDataPartitionRequest(partitionType, volName string, ID uint64) (req *proto.CreateDataPartitionRequest) {
	req = &proto.CreateDataPartitionRequest{
		PartitionType: partitionType,
		PartitionId:   ID,
		PartitionSize: util.DefaultDataPartitionSize,
		VolumeId:      volName,
	}
	return
}

func newDeleteDataPartitionRequest(ID uint64) (req *proto.DeleteDataPartitionRequest) {
	req = &proto.DeleteDataPartitionRequest{
		PartitionId: ID,
	}
	return
}

func newLoadDataPartitionMetricRequest(partitionType string, ID uint64) (req *proto.LoadDataPartitionRequest) {
	req = &proto.LoadDataPartitionRequest{
		PartitionType: partitionType,
		PartitionId:   ID,
	}
	return
}

func UnmarshalTaskResponse(task *proto.AdminTask) (err error) {
	bytes, err := json.Marshal(task.Response)
	if err != nil {
		return
	}
	var response interface{}
	switch task.OpCode {
	case proto.OpDataNodeHeartbeat:
		response = &proto.DataNodeHeartBeatResponse{}
	case proto.OpCreateDataPartition:
		response = &proto.CreateDataPartitionResponse{}
	case proto.OpDeleteDataPartition:
		response = &proto.DeleteDataPartitionResponse{}
	case proto.OpLoadDataPartition:
		response = &proto.LoadDataPartitionResponse{}
	case proto.OpDeleteFile:
		response = &proto.DeleteFileResponse{}
	case proto.OpMetaNodeHeartbeat:
		response = &proto.MetaNodeHeartbeatResponse{}
	case proto.OpCreateMetaPartition:
		response = &proto.CreateMetaPartitionResponse{}
	case proto.OpDeleteMetaPartition:
		response = &proto.DeleteMetaPartitionResponse{}
	case proto.OpUpdateMetaPartition:
		response = &proto.UpdateMetaPartitionResponse{}
	case proto.OpLoadMetaPartition:
		response = &proto.LoadMetaPartitionMetricResponse{}
	case proto.OpOfflineMetaPartition:
		response = &proto.MetaPartitionOfflineResponse{}

	default:
		log.LogError(fmt.Sprintf("unknown operate code(%v)", task.OpCode))
	}

	if response == nil {
		return fmt.Errorf("UnmarshalTaskResponse failed")
	}
	if err = json.Unmarshal(bytes, response); err != nil {
		return
	}
	task.Response = response
	return
}

func contains(arr []string, element string) (ok bool) {
	if arr == nil || len(arr) == 0 {
		return
	}

	for _, e := range arr {
		if e == element {
			ok = true
			break
		}
	}
	return
}

func Warn(clusterID, msg string) {
	umpKey := fmt.Sprintf("%s_%s", clusterID, UmpModuleName)
	WarnBySpecialUmpKey(umpKey, msg)
}

func WarnBySpecialUmpKey(umpKey, msg string) {
	log.LogWarn(msg)
	ump.Alarm(umpKey, msg)
}

func matchKey(serverKey, clientKey string) bool {
	h := md5.New()
	_, err := h.Write([]byte(serverKey))
	if err != nil {
		log.LogWarnf("action[matchKey] write server key[%v] failed,err[%v]", serverKey, err)
		return false
	}
	cipherStr := h.Sum(nil)
	clientKey = strings.ToLower(strings.TrimSpace(clientKey))
	serverKey = strings.ToLower(strings.TrimSpace(hex.EncodeToString(cipherStr)))
	return clientKey == serverKey
}
