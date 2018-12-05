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
	"container/list"
	"fmt"
	"net"
	"sync"

	"github.com/tiglabs/containerfs/proto"
)

var single = struct{}{}

type MessageHandler struct {
	listMux     sync.RWMutex
	sentList    *list.List
	handleCh    chan struct{}
	requestCh   chan *Packet
	replyCh     chan *Packet
	inConn      *net.TCPConn
	isClean     bool
	exitC       chan bool
	exited      bool
	exitedMu    sync.RWMutex
	connectMap  map[string]*net.TCPConn
	connectLock sync.RWMutex
}

func NewMsgHandler(inConn *net.TCPConn) *MessageHandler {
	m := new(MessageHandler)
	m.sentList = list.New()
	m.handleCh = make(chan struct{}, RequestChanSize)
	m.requestCh = make(chan *Packet, RequestChanSize)
	m.replyCh = make(chan *Packet, RequestChanSize)
	m.exitC = make(chan bool, 100)
	m.inConn = inConn
	m.connectMap = make(map[string]*net.TCPConn)

	return m
}

func (msgH *MessageHandler) RenewList(isHeadNode bool) {
	if !isHeadNode {
		msgH.sentList = list.New()
	}
}

func (msgH *MessageHandler) Stop() {
	msgH.exitedMu.Lock()
	defer msgH.exitedMu.Unlock()
	if !msgH.exited {
		if msgH.exitC != nil {
			close(msgH.exitC)
		}
		msgH.exited = true
	}

}

func (msgH *MessageHandler) ExitSign() {
	msgH.exitedMu.Lock()
	defer msgH.exitedMu.Unlock()
	if !msgH.exited {
		if msgH.exitC != nil {
			close(msgH.exitC)
		}
		msgH.exited = true
	}
}

func (msgH *MessageHandler) AllocateNextConn(pkg *Packet, index int) (err error) {
	var conn *net.TCPConn
	if pkg.StoreMode == proto.NormalExtentMode && pkg.IsWriteOperation() {
		key := fmt.Sprintf("%v_%v_%v", pkg.PartitionID, pkg.FileID, pkg.NextAddrs[index])
		msgH.connectLock.RLock()
		conn := msgH.connectMap[key]
		msgH.connectLock.RUnlock()
		if conn == nil {
			conn, err = gConnPool.Get(pkg.NextAddrs[index])
			if err != nil {
				return
			}
			msgH.connectLock.Lock()
			msgH.connectMap[key] = conn
			msgH.connectLock.Unlock()
		}
		pkg.useConnectMap = true
		pkg.NextConns[index] = conn
	} else {
		conn, err = gConnPool.Get(pkg.NextAddrs[index])
		if err != nil {
			return
		}
		pkg.NextConns[index] = conn
	}
	return nil
}

func (msgH *MessageHandler) checkReplyAvail(reply *Packet, index int) (err error) {
	msgH.listMux.Lock()
	defer msgH.listMux.Unlock()

	for e := msgH.sentList.Front(); e != nil; e = e.Next() {
		request := e.Value.(*Packet)
		if reply.ReqID == request.ReqID {
			return
		}
		return fmt.Errorf(ActionCheckReplyAvail+" request (%v) reply(%v) from %v localaddr %v"+
			" remoteaddr %v requestCrc(%v) replyCrc(%v)", request.GetUniqueLogId(), reply.GetUniqueLogId(), request.NextAddrs[index],
			request.NextConns[index].LocalAddr().String(), request.NextConns[index].RemoteAddr().String(), request.Crc, reply.Crc)
	}

	return
}

func (msgH *MessageHandler) GetListElement() (e *list.Element) {
	msgH.listMux.RLock()
	e = msgH.sentList.Front()
	msgH.listMux.RUnlock()

	return
}

func (msgH *MessageHandler) PushListElement(e *Packet) {
	msgH.listMux.Lock()
	msgH.sentList.PushBack(e)
	msgH.listMux.Unlock()
}

func (msgH *MessageHandler) ClearReqs(s *DataNode) {
	msgH.listMux.Lock()
	for e := msgH.sentList.Front(); e != nil; e = e.Next() {
		request := e.Value.(*Packet)
		request.forceDestoryAllConnect()
		s.leaderPutTinyExtentToStore(request)

	}
	replys := len(msgH.replyCh)
	for i := 0; i < replys; i++ {
		<-msgH.replyCh
	}
	msgH.sentList = list.New()
	msgH.connectLock.RLock()
	for _, conn := range msgH.connectMap {
		conn.Close()
	}
	msgH.connectLock.RUnlock()
	msgH.connectLock.Lock()
	msgH.connectMap = make(map[string]*net.TCPConn, 0)
	msgH.connectLock.Unlock()
	msgH.listMux.Unlock()
}

func (msgH *MessageHandler) DelListElement(reply *Packet) (success bool) {
	msgH.listMux.Lock()
	defer msgH.listMux.Unlock()
	for e := msgH.sentList.Front(); e != nil; e = e.Next() {
		request := e.Value.(*Packet)
		if reply.ReqID != request.ReqID || reply.PartitionID != request.PartitionID ||
			reply.Offset != request.Offset || reply.Crc != request.Crc || reply.FileID != request.FileID {
			request.forceDestoryAllConnect()
			request.PackErrorBody(ActionReceiveFromNext, fmt.Sprintf("unknow expect reply"))
			break
		}
		msgH.sentList.Remove(e)
		success = true
		msgH.replyCh <- reply
	}

	return
}
