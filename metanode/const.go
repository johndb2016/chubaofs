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
	"github.com/chubaofs/cfs/proto"
	"github.com/chubaofs/cfs/third_party/juju/errors"
	"time"
)

const (
	StateStandby uint32 = iota
	StateStart
	StateRunning
	StateShutdown
	StateStopped
)

// Type alias.
type (
	// Master -> MetaNode  create metaPartition request struct
	CreateMetaRangeReq = proto.CreateMetaPartitionRequest
	// MetaNode -> Master create metaPartition response struct
	CreateMetaRangeResp = proto.CreateMetaPartitionResponse
	// Client -> MetaNode create inode request struct
	CreateInoReq = proto.CreateInodeRequest
	// MetaNode -> Client create inode response struct
	CreateInoResp = proto.CreateInodeResponse
	// Client -> MetaNode create Link Request
	LinkInodeReq = proto.LinkInodeRequest
	// MetaNode -> Client create Link Response
	LinkInodeResp = proto.LinkInodeResponse
	// Client -> MetaNode delete inode request struct
	DeleteInoReq = proto.DeleteInodeRequest
	// MetaNode -> Client delete inode response
	DeleteInoResp = proto.DeleteInodeResponse
	// Client -> MetaNode create Dentry request struct
	CreateDentryReq = proto.CreateDentryRequest
	// Client -> MetaNode delete Dentry request struct
	DeleteDentryReq = proto.DeleteDentryRequest
	// MetaNode -> Client delete Dentry response struct
	DeleteDentryResp = proto.DeleteDentryResponse
	// Client -> MetaNode updateDentry request struct
	UpdateDentryReq = proto.UpdateDentryRequest
	// MetaNode -> Client updateDentry response struct
	UpdateDentryResp = proto.UpdateDentryResponse
	// Client -> MetaNode read dir request struct
	ReadDirReq = proto.ReadDirRequest
	// MetaNode -> Client read dir response struct
	ReadDirResp = proto.ReadDirResponse
	// MetaNode -> Client lookup
	LookupReq = proto.LookupRequest
	// Client -> MetaNode lookup
	LookupResp = proto.LookupResponse
	// Client -> MetaNode open file request struct
	OpenReq = proto.OpenRequest
	// Client -> MetaNode
	InodeGetReq = proto.InodeGetRequest
	// Client -> MetaNode
	InodeGetReqBatch = proto.BatchInodeGetRequest
	// Master -> MetaNode
	UpdatePartitionReq = proto.UpdateMetaPartitionRequest
	// MetaNode -> Master
	UpdatePartitionResp = proto.UpdateMetaPartitionResponse
	// Client -> MetaNode
	ExtentsTruncateReq = proto.TruncateRequest
	// MetaNode -> Client
	ExtentsTruncateResp = proto.TruncateResponse

	// Client -> MetaNode
	EvictInodeReq = proto.EvictInodeRequest
)

// For use when raftStore store and application apply
const (
	opCreateInode uint32 = iota
	opDeleteInode
	opCreateDentry
	opDeleteDentry
	opOpen
	opDeletePartition
	opUpdatePartition
	opOfflinePartition
	opExtentsAdd
	opStoreTick
	startStoreTick
	stopStoreTick
	opUpdateDentry
	opFSMExtentTruncate
	opFSMCreateLinkInode
	opFSMEvictInode
	opFSMInternalDeleteInode
)

var (
	masterAddrs   []string
	curMasterAddr string
	UMPKey        string
)

var (
	ErrNonLeader = errors.New("non leader")
	ErrNotLeader = errors.New("not leader")
)

// default config
const (
	defaultMetaDir = "metaDir"
	defaultRaftDir = "raftDir"
)

const (
	metaNodeURL     = "/metaNode/add"
	metaNodeGetName = "/admin/getIp"
)

// Configuration keys
const (
	cfgListen            = "listen"
	cfgMetaDir           = "metaDir"
	cfgRaftDir           = "raftDir"
	cfgMasterAddrs       = "masterAddrs"
	cfgRaftHeartbeatPort = "raftHeartbeatPort"
	cfgRaftReplicatePort = "raftReplicatePort"
)

const (
	storeTimeTicker = time.Minute * 5
)
