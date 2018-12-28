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
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/tiglabs/containerfs/master"
	"github.com/tiglabs/containerfs/proto"
	"github.com/tiglabs/containerfs/storage"
	"github.com/tiglabs/containerfs/third_party/juju/errors"
	"github.com/tiglabs/containerfs/util"
	"github.com/tiglabs/containerfs/util/log"
	"github.com/tiglabs/containerfs/util/ump"
)

var (
	ErrorUnknownOp = errors.New("unknown opcode")
)

func (s *DataNode) operatePacket(pkg *Packet, c *net.TCPConn) {
	orgSize := pkg.Size
	umpKey := fmt.Sprintf("%s_datanode_%s", s.clusterId, pkg.GetOpMsg())
	tpObject := ump.BeforeTP(umpKey)
	start := time.Now().UnixNano()
	var err error
	defer func() {
		resultSize := pkg.Size
		pkg.Size = orgSize
		if pkg.IsErrPack() {
			err = fmt.Errorf("op[%v] error[%v]", pkg.GetOpMsg(), string(pkg.Data[:resultSize]))
			logContent := fmt.Sprintf("action[operatePacket] %v.",
				pkg.ActionMsg(pkg.GetOpMsg(), c.RemoteAddr().String(), start, err))
			log.LogErrorf(logContent)
		} else {
			logContent := fmt.Sprintf("action[operatePacket] %v.",
				pkg.ActionMsg(pkg.GetOpMsg(), c.RemoteAddr().String(), start, nil))
			switch pkg.Opcode {
			case proto.OpStreamRead, proto.OpRead:
				log.LogRead(logContent)
			case proto.OpWrite:
				log.LogWrite(logContent)
			default:
				log.LogInfo(logContent)
			}
		}
		pkg.Size = resultSize
		ump.AfterTP(tpObject, err)
	}()
	switch pkg.Opcode {
	case proto.OpCreateFile:
		s.handleCreateFile(pkg)
	case proto.OpWrite:
		s.handleWrite(pkg)
	case proto.OpRead:
		s.handleRead(pkg)
	case proto.OpStreamRead:
		s.handleStreamRead(pkg, c)
	case proto.OpMarkDelete:
		s.handleMarkDelete(pkg)
	case proto.OpNotifyExtentRepair:
		s.handleNotifyExtentRepair(pkg)
	case proto.OpGetWatermark:
		s.handleGetWatermark(pkg)
	case proto.OpExtentStoreGetAllWaterMark:
		s.handleExtentStoreGetAllWatermark(pkg)
	case proto.OpCreateDataPartition:
		s.handleCreateDataPartition(pkg)
	case proto.OpLoadDataPartition:
		s.handleLoadDataPartition(pkg)
	case proto.OpDeleteDataPartition:
		s.handleDeleteDataPartition(pkg)
	case proto.OpDataNodeHeartbeat:
		s.handleHeartbeats(pkg)
	case proto.OpGetDataPartitionMetrics:
		s.handleGetDataPartitionMetrics(pkg)
	default:
		pkg.PackErrorBody(ErrorUnknownOp.Error(), ErrorUnknownOp.Error()+strconv.Itoa(int(pkg.Opcode)))
	}

	return
}

// Handle OpCreateFile packet.
func (s *DataNode) handleCreateFile(pkg *Packet) {
	var err error
	defer func() {
		if err != nil {
			err = errors.Annotatef(err, "Request[%v] CreateFile Error", pkg.GetUniqueLogId())
			pkg.PackErrorBody(LogCreateFile, err.Error())
		} else {
			pkg.PackOkReply()
		}
	}()
	if pkg.DataPartition.Available() <= 0 {
		err = storage.ErrSyscallNoSpace
		return
	}
	if pkg.DataPartition.Disk().Status == proto.ReadOnly {
		err = storage.ErrSyscallNoSpace
		return
	}
	var ino uint64
	if len(pkg.Data) >= 8 && pkg.Size >= 8 {
		ino = binary.BigEndian.Uint64(pkg.Data)
	}
	err = pkg.DataPartition.GetExtentStore().Create(pkg.FileID, ino)
	return
}

// Handle OpCreateDataPartition packet.
func (s *DataNode) handleCreateDataPartition(pkg *Packet) {
	var (
		err error
	)
	defer func() {
		if err != nil {
			err = errors.Annotatef(err, "Request[%v] CreateDataPartition Error", pkg.GetUniqueLogId())
			pkg.PackErrorBody(ActionCreateDataPartition, err.Error())
		} else {
			pkg.PackOkReply()
		}
	}()
	task := &proto.AdminTask{}
	if err = json.Unmarshal(pkg.Data, task); err != nil {
		err = fmt.Errorf("cannnot unmashal adminTask")
		return
	}
	request := &proto.CreateDataPartitionRequest{}
	if task.OpCode != proto.OpCreateDataPartition {
		err = fmt.Errorf("from master Task[%v] failed,error unavali opcode(%v)", task.ToString(), task.OpCode)
		return
	}

	bytes, err := json.Marshal(task.Request)
	if err != nil {
		err = fmt.Errorf("from master Task[%v] cannot unmashal CreateDataPartition", task.ToString())
		return
	}
	if err = json.Unmarshal(bytes, request); err != nil {
		err = fmt.Errorf("from master Task[%v] cannot unmash CreateDataPartitionRequest struct", task.ToString())
		return
	}
	if _, err = s.space.CreatePartition(request.VolumeId, uint32(request.PartitionId),
		request.PartitionSize, request.PartitionType); err != nil {
		err = fmt.Errorf("from master Task[%v] cannot unmash CreateDataPartitionRequest struct", task.ToString())
		return
	}

	return
}

// Handle OpHeartbeat packet.
func (s *DataNode) handleHeartbeats(pkg *Packet) {
	var err error
	task := &proto.AdminTask{}
	json.Unmarshal(pkg.Data, task)
	pkg.PackOkReply()

	request := &proto.HeartBeatRequest{}
	response := &proto.DataNodeHeartBeatResponse{}

	s.fillHeartBeatResponse(response)

	if task.OpCode == proto.OpDataNodeHeartbeat {
		bytes, _ := json.Marshal(task.Request)
		json.Unmarshal(bytes, request)
		response.Status = proto.TaskSuccess
		MasterHelper.AddNode(request.MasterAddr)
	} else {
		response.Status = proto.TaskFail
		response.Result = "illegal opcode"
	}
	task.Response = response
	data, err := json.Marshal(task)
	if err != nil {
		log.LogErrorf("action[heartbeat] err[%v].", err)
		return
	}
	_, err = MasterHelper.Request("POST", master.DataNodeResponse, nil, data)
	if err != nil {
		err = errors.Annotatef(err, "heartbeat to master[%v] failed.", request.MasterAddr)
		log.LogErrorf("action[handleHeartbeats] err[%v].", err)
		log.LogErrorf(errors.ErrorStack(err))
		return
	}
	log.LogDebugf("action[handleHeartbeats] report data len[%v] to master success.", len(data))
}

// Handle OpDeleteDataPartition packet.
func (s *DataNode) handleDeleteDataPartition(pkg *Packet) {
	task := &proto.AdminTask{}
	json.Unmarshal(pkg.Data, task)
	pkg.PackOkReply()
	request := &proto.DeleteDataPartitionRequest{}
	response := &proto.DeleteDataPartitionResponse{}
	if task.OpCode == proto.OpDeleteDataPartition {
		bytes, _ := json.Marshal(task.Request)
		err := json.Unmarshal(bytes, request)
		if err != nil {
			response.PartitionId = uint64(request.PartitionId)
			response.Status = proto.TaskFail
			response.Result = err.Error()
			log.LogErrorf("action[handleDeleteDataPartition] from master Task[%v] failed, err[%v]", task.ToString(), err)
		} else {
			s.space.DeletePartition(uint32(request.PartitionId))
			response.PartitionId = uint64(request.PartitionId)
			response.Status = proto.TaskSuccess
		}
	} else {
		response.PartitionId = uint64(request.PartitionId)
		response.Status = proto.TaskFail
		response.Result = "illegal opcode "
		log.LogErrorf("action[handleDeleteDataPartition] from master Task[%v] failed, err[%v].", task.ToString(), response.Result)
	}
	task.Response = response
	data, _ := json.Marshal(task)
	_, err := MasterHelper.Request("POST", master.DataNodeResponse, nil, data)
	if err != nil {
		err = errors.Annotatef(err, "delete dataPartition failed,partitionId[%v]", request.PartitionId)
		log.LogErrorf("action[handleDeleteDataPartition] err[%v].", err)
	}
	log.LogInfof(fmt.Sprintf("action[handleDeleteDataPartition] %v error(%v)", request.PartitionId, string(data)))
}

// Handle OpLoadDataPartition packet.
func (s *DataNode) handleLoadDataPartition(pkg *Packet) {
	task := &proto.AdminTask{}
	json.Unmarshal(pkg.Data, task)
	pkg.PackOkReply()
	request := &proto.LoadDataPartitionRequest{}
	response := &proto.LoadDataPartitionResponse{}
	if task.OpCode == proto.OpLoadDataPartition {
		bytes, _ := json.Marshal(task.Request)
		json.Unmarshal(bytes, request)
		dp := s.space.GetPartition(uint32(request.PartitionId))
		if dp == nil {
			response.Status = proto.TaskFail
			response.PartitionId = uint64(request.PartitionId)
			response.Result = fmt.Sprintf("dataPartition[%v] not found", request.PartitionId)
			log.LogErrorf("from master Task[%v] failed,error[%v]", task.ToString(), response.Result)
		} else {
			response = dp.(*dataPartition).Load()
			response.PartitionId = uint64(request.PartitionId)
			response.Status = proto.TaskSuccess
		}
	} else {
		response.PartitionId = uint64(request.PartitionId)
		response.Status = proto.TaskFail
		response.Result = "illegal opcode "
		log.LogErrorf("from master Task[%v] failed,error[%v]", task.ToString(), response.Result)
	}
	task.Response = response
	data, err := json.Marshal(task)
	if err != nil {
		response.PartitionId = uint64(request.PartitionId)
		response.Status = proto.TaskFail
		response.Result = err.Error()
		log.LogErrorf("from master Task[%v] failed,error[%v]", task.ToString(), response.Result)
	}
	_, err = MasterHelper.Request("POST", master.DataNodeResponse, nil, data)
	if err != nil {
		err = errors.Annotatef(err, "load dataPartition failed,partitionId[%v]", request.PartitionId)
		log.LogError(errors.ErrorStack(err))
	}
}

// Handle OpMarkDelete packet.
// Handle OpMarkDelete packet.
func (s *DataNode) handleMarkDelete(pkg *Packet) {
	var (
		err error
	)
	if pkg.StoreMode == proto.TinyExtentMode {
		ext := new(proto.ExtentKey)
		err = json.Unmarshal(pkg.Data, ext)
		if err == nil {
			err = pkg.DataPartition.GetExtentStore().MarkDelete(pkg.FileID, int64(ext.ExtentOffset), int64(ext.Size))
		}
	} else {
		err = pkg.DataPartition.GetExtentStore().MarkDelete(pkg.FileID, 0, 0)
	}
	if err != nil {
		err = errors.Annotatef(err, "Request(%v) MarkDelete Error", pkg.GetUniqueLogId())
		pkg.PackErrorBody(LogMarkDel, err.Error())
	} else {
		pkg.PackOkReply()
	}

	return
}

// Handle OpWrite packet.
func (s *DataNode) handleWrite(pkg *Packet) {
	var err error
	defer func() {
		if err != nil {
			err = errors.Annotatef(err, "Request[%v] Write Error", pkg.GetUniqueLogId())
			pkg.PackErrorBody(LogWrite, err.Error())
		} else {
			pkg.PackOkReply()
		}
	}()

	if pkg.DataPartition.Available() <= 0 {
		err = storage.ErrSyscallNoSpace
		return
	}
	if pkg.DataPartition.Disk().Status == proto.ReadOnly {
		err = storage.ErrSyscallNoSpace
		return
	}
	err = pkg.DataPartition.GetExtentStore().Write(pkg.FileID, pkg.Offset, int64(pkg.Size), pkg.Data, pkg.Crc)
	s.addDiskErrs(pkg.PartitionID, err, WriteFlag)
	if err == nil && pkg.Opcode == proto.OpWrite && pkg.Size == util.BlockSize {
		proto.Buffers.Put(pkg.Data)
	}
	return
}

// Handle OpRead packet.
func (s *DataNode) handleRead(pkg *Packet) {
	pkg.Data = make([]byte, pkg.Size)
	var err error
	pkg.Crc, err = pkg.DataPartition.GetExtentStore().Read(pkg.FileID, pkg.Offset, int64(pkg.Size), pkg.Data)
	s.addDiskErrs(pkg.PartitionID, err, ReadFlag)
	if err == nil {
		pkg.PackOkReadReply()
	} else {
		pkg.PackErrorBody(LogRead, err.Error())
	}

	return
}

// Handle OpStreamRead packet.
func (s *DataNode) handleStreamRead(request *Packet, connect net.Conn) {
	var (
		err error
	)
	needReplySize := request.Size
	offset := request.Offset
	store := request.DataPartition.GetExtentStore()
	umpKey := fmt.Sprintf("%s_datanode_%s", s.clusterId, "Read")
	reply := NewStreamReadResponsePacket(request.ReqID, request.PartitionID, request.FileID)
	reply.StartT = time.Now().UnixNano()
	for {
		if needReplySize <= 0 {
			break
		}
		err = nil
		currReadSize := uint32(util.Min(int(needReplySize), util.ReadBlockSize))
		if currReadSize == util.ReadBlockSize {
			reply.Data, _ = proto.Buffers.Get(util.ReadBlockSize)
		} else {
			reply.Data = make([]byte, currReadSize)
		}
		tpObject := ump.BeforeTP(umpKey)
		reply.Offset = offset
		reply.Crc, err = store.Read(reply.FileID, offset, int64(currReadSize), reply.Data)
		ump.AfterTP(tpObject, err)
		if err != nil {
			reply.PackErrorBody(ActionStreamRead, err.Error())
			request.PackErrorBody(ActionStreamRead, err.Error())
			if err = reply.WriteToConn(connect); err != nil {
				err = fmt.Errorf(reply.ActionMsg(ActionWriteToCli, connect.RemoteAddr().String(),
					reply.StartT, err))
				log.LogErrorf(err.Error())
			}
			return
		}
		reply.Size = uint32(currReadSize)
		reply.ResultCode = proto.OpOk
		if err = reply.WriteToConn(connect); err != nil {
			err = fmt.Errorf(reply.ActionMsg(ActionWriteToCli, connect.RemoteAddr().String(),
				reply.StartT, err))
			log.LogErrorf(err.Error())
			connect.Close()
			request.PackErrorBody(ActionStreamRead, err.Error())
			return
		}
		needReplySize -= currReadSize
		offset += int64(currReadSize)
		if currReadSize == util.ReadBlockSize {
			proto.Buffers.Put(reply.Data)
		}
	}
	request.PackOkReply()
	return
}

// Handle OpGetWatermark packet.
func (s *DataNode) handleGetWatermark(pkg *Packet) {
	var buf []byte
	var (
		fInfo *storage.FileInfo
		err   error
	)
	fInfo, err = pkg.DataPartition.GetExtentStore().GetWatermark(pkg.FileID, false)
	if err != nil {
		err = errors.Annotatef(err, "Request[%v] handleGetWatermark Error", pkg.GetUniqueLogId())
		pkg.PackErrorBody(LogGetWm, err.Error())
	} else {
		buf, err = json.Marshal(fInfo)
		pkg.PackOkWithBody(buf)
	}

	return
}

func (s *DataNode) handleExtentStoreGetAllWatermark(pkg *Packet) {
	var (
		buf       []byte
		fInfoList []*storage.FileInfo
		err       error
	)
	store := pkg.DataPartition.GetExtentStore()
	if pkg.StoreMode == proto.NormalExtentMode {
		fInfoList, err = store.GetAllWatermark(storage.GetStableExtentFilter())
	} else {
		extents := make([]uint64, 0)
		err = json.Unmarshal(pkg.Data, &extents)
		if err == nil {
			fInfoList, err = store.GetAllWatermark(storage.GetStableTinyExtentFilter(extents))
		}
	}
	if err != nil {
		err = errors.Annotatef(err, "Request(%v) handleExtentStoreGetAllWatermark Error", pkg.GetUniqueLogId())
		pkg.PackErrorBody(LogGetAllWm, err.Error())
	} else {
		buf, err = json.Marshal(fInfoList)
		pkg.PackOkWithBody(buf)
	}
	return
}

// Handle OpNotifyRepair packet.
func (s *DataNode) handleNotifyExtentRepair(pkg *Packet) {
	var (
		err error
	)
	mf := NewMemberFileMetas()
	err = json.Unmarshal(pkg.Data, mf)
	if err != nil {
		pkg.PackErrorBody(LogRepair, err.Error())
		return
	}
	pkg.DataPartition.MergeExtentStoreRepair(mf)
	pkg.PackOkReply()
	return
}

func (s *DataNode) handleGetDataPartitionMetrics(pkg *Packet) {
	dp := pkg.DataPartition.(*dataPartition)
	data, err := json.Marshal(dp.runtimeMetrics)
	if err != nil {
		err = errors.Annotatef(err, "dataPartionMetrics[%v] json mashal failed", dp.ID())
		pkg.PackErrorBody(ActionGetDataPartitionMetrics, err.Error())
		return
	} else {
		pkg.PackOkWithBody(data)
	}
}
