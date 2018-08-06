// Copyright 2018 The ChuBao Authors.
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
	"github.com/chubaoio/cbfs/proto"
)

/*check File: recover File,if File lack or timeOut report or crc bad*/
func (partition *DataPartition) checkFile(clusterID string) {
	partition.Lock()
	defer partition.Unlock()
	liveReplicas := partition.getLiveReplicas(DefaultDataPartitionTimeOutSec)
	if len(liveReplicas) == 0 {
		return
	}

	switch partition.PartitionType {
	case proto.ExtentPartition:
		partition.checkExtentFile(liveReplicas, clusterID)
	case proto.TinyPartition:
		partition.checkChunkFile(liveReplicas, clusterID)
	}

	return
}

func (partition *DataPartition) checkChunkFile(liveReplicas []*DataReplica, clusterID string) {
	for _, fc := range partition.FileInCoreMap {
		fc.generateFileCrcTask(partition.PartitionID, liveReplicas, proto.TinyPartition, clusterID)
	}
	return
}

func (partition *DataPartition) checkExtentFile(liveReplicas []*DataReplica, clusterID string) (tasks []*proto.AdminTask) {
	for _, fc := range partition.FileInCoreMap {
		fc.generateFileCrcTask(partition.PartitionID, liveReplicas, proto.ExtentPartition, clusterID)
	}
	return
}
