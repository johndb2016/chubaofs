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

const (
	Standby uint32 = iota
	Start
	Running
	Shutdown
	Stopped
)

const (
	RequestChanSize = 10240
)

const (
	ActionSendToNext                                 = "ActionSendToNext"
	LocalProcessAddr                                 = "LocalProcess"
	ActionReceiveFromNext                            = "ActionReceiveFromNext"
	ActionStreamRead                                 = "ActionStreamRead"
	ActionWriteToCli                                 = "ActionWriteToCli"
	ActionGetDataPartitionMetrics                    = "ActionGetDataPartitionMetrics"
	ActionCheckAndAddInfos                           = "ActionCheckAndAddInfos"
	ActionCheckBlobFileInfo                          = "ActionCheckBlobFileInfo"
	ActionPostToMaster                               = "ActionPostToMaster"
	ActionFollowerRequireBlobFileRepairCmd           = "ActionFollowerRequireBlobFileRepairCmd"
	ActionLeaderToFollowerOpRepairReadPackBuffer     = "ActionLeaderToFollowerOpRepairReadPackBuffer"
	ActionLeaderToFollowerOpRepairReadSendPackBuffer = "ActionLeaderToFollowerOpRepairReadSendPackBuffer"

	ActionGetFollowers    = "ActionGetFollowers"
	ActionCheckReplyAvail = "ActionCheckReplyAvail"
)

const (
	InFlow = iota
	OutFlow
)

const (
	NetType = "tcp"
)

//pack cmd response
const (
	ReadFlag         = 1
	WriteFlag        = 2
	MaxActiveExtents = 10000
)

const (
	ConnIsNullErr              = "ConnIsNullErr"
	UpdateReplicationHostsTime = 60
	SimultaneouslyRecoverFiles = 7
	UpdatePartitionSizeTime    = 300
)

const (
	LogStats      = "Stats:"
	LogCreateFile = "CRF:"
	LogMarkDel    = "MDEL:"
	LogGetWm      = "WM:"
	LogGetAllWm   = "AllWM:"
	LogWrite      = "WR:"
	LogRead       = "RD:"
	LogRepair     = "Repair:"
)
