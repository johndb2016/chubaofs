package mocktest

import (
	"net"
	"io/ioutil"
	"github.com/chubaofs/chubaofs/proto"
	"fmt"
	"net/http"
	"encoding/json"
	"github.com/chubaofs/chubaofs/util"
	"strconv"
	"strings"
	"bytes"
	"sync"
)

type MockMetaServer struct {
	NodeID     uint64
	TcpAddr    string
	partitions map[uint64]*MockMetaPartition // Key: metaRangeId, Val: metaPartition
	sync.RWMutex
}

func NewMockMetaServer(addr string) *MockMetaServer {
	mms := &MockMetaServer{TcpAddr: addr, partitions: make(map[uint64]*MockMetaPartition, 0)}
	return mms
}

func (mms *MockMetaServer) Start() {
	mms.register()
	go mms.start()
}

func (mms *MockMetaServer) register() {
	reqUrl := fmt.Sprintf("%v?addr=%v", urlAddMetaNode, mms.TcpAddr)
	resp, err := http.Get(reqUrl)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(body))
	nodeIDStr := strings.TrimSpace(string(body))
	if nodeIDStr == "" {
		panic(fmt.Errorf("nodeIDStr is nil"))
	}
	mms.NodeID, err = strconv.ParseUint(nodeIDStr, 10, 64)
	if err != nil {
		panic(err)
	}
}

func (mms *MockMetaServer) start() {
	s := strings.Split(mms.TcpAddr, ColonSeparator)
	listener, err := net.Listen("tcp", ":"+s[1])
	if err != nil {
		panic(err)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			fmt.Printf("accept conn occurred error,err is [%v]", err)
		}
		go mms.serveConn(conn)
	}
}

func (mms *MockMetaServer) serveConn(rc net.Conn) {
	fmt.Printf("remote[%v],local[%v]\n", rc.RemoteAddr(), rc.LocalAddr())
	conn, ok := rc.(*net.TCPConn)
	if !ok {
		rc.Close()
		return
	}
	conn.SetKeepAlive(true)
	conn.SetNoDelay(true)
	req := proto.NewPacket()
	err := req.ReadFromConn(conn, proto.NoReadDeadlineTime)
	if err != nil {
		fmt.Printf("remote [%v] err is [%v]\n", conn.RemoteAddr(), err)
		return
	}
	fmt.Printf("remote [%v] req [%v]\n", conn.RemoteAddr(), req.GetOpMsg())
	adminTask := &proto.AdminTask{}
	decode := json.NewDecoder(bytes.NewBuffer(req.Data))
	decode.UseNumber()
	if err = decode.Decode(adminTask); err != nil {
		responseAckErrToMaster(conn, req, err)
		return
	}
	switch req.Opcode {
	case proto.OpCreateMetaPartition:
		err = mms.handleCreateMetaPartition(conn, req, adminTask)
		fmt.Printf("meta node [%v] create meta partition,err:%v\n", mms.TcpAddr, err)
	case proto.OpMetaNodeHeartbeat:
		err = mms.handleHeartbeats(conn, req, adminTask)
		fmt.Printf("meta node [%v] heartbeat,err:%v\n", mms.TcpAddr, err)
	case proto.OpDeleteMetaPartition:
		err = mms.handleDeleteMetaPartition(conn, req, adminTask)
		fmt.Printf("meta node [%v] delete meta partition,err:%v\n", mms.TcpAddr, err)
	case proto.OpUpdateMetaPartition:
		err = mms.handleUpdateMetaPartition(conn, req, adminTask)
		fmt.Printf("meta node [%v] update meta partition,err:%v\n", mms.TcpAddr, err)
	case proto.OpLoadMetaPartition:
		err = mms.handleLoadMetaPartition(conn, req, adminTask)
		fmt.Printf("meta node [%v] load meta partition,err:%v\n", mms.TcpAddr, err)
	case proto.OpOfflineMetaPartition:
		err = mms.handleOfflineMetaPartition(conn, req, adminTask)
		fmt.Printf("meta node [%v] offline meta partition,err:%v\n", mms.TcpAddr, err)
	default:
		fmt.Printf("unknown code [%v]\n", req.Opcode)
	}
}

func (mms *MockMetaServer) handleCreateMetaPartition(conn net.Conn, p *proto.Packet, adminTask *proto.AdminTask) (err error) {
	defer func() {
		if err != nil {
			responseAckErrToMaster(conn, p, err)
		} else {
			responseAckOKToMaster(conn, p)
		}
	}()
	// Marshal request body.
	requestJson, err := json.Marshal(adminTask.Request)
	if err != nil {
		return
	}
	// Unmarshal request to entity
	req := &proto.CreateMetaPartitionRequest{}
	if err = json.Unmarshal(requestJson, req); err != nil {
		return
	}
	// Create new  metaPartition.
	partition := &MockMetaPartition{
		PartitionID: req.PartitionID,
		VolName:     req.VolName,
		Start:       req.Start,
		End:         req.End,
		Cursor:      req.Start,
		Members:     req.Members,
	}
	mms.Lock()
	mms.partitions[req.PartitionID] = partition
	mms.Unlock()
	return
}

// Handle OpHeartbeat packet.
func (mms *MockMetaServer) handleHeartbeats(conn net.Conn, p *proto.Packet, adminTask *proto.AdminTask) (err error) {
	// For ack to master
	responseAckOKToMaster(conn, p)
	var (
		req     = &proto.HeartBeatRequest{}
		resp    = &proto.MetaNodeHeartbeatResponse{}
		reqData []byte
	)
	reqData, err = json.Marshal(adminTask.Request)
	if err != nil {
		resp.Status = proto.TaskFail
		resp.Result = err.Error()
		goto end
	}
	if err = json.Unmarshal(reqData, req); err != nil {
		resp.Status = proto.TaskFail
		resp.Result = err.Error()
		goto end
	}
	resp.Total = 10 * util.GB
	resp.Used = 1 * util.GB
	// every partition used
	mms.RLock()
	for id, partition := range mms.partitions {
		mpr := &proto.MetaPartitionReport{
			PartitionID: id,
			Start:       partition.Start,
			End:         partition.End,
			Status:      proto.ReadWrite,
			MaxInodeID:  1,
		}
		mpr.Status = proto.ReadWrite
		mpr.IsLeader = true
		resp.MetaPartitionInfo = append(resp.MetaPartitionInfo, mpr)
	}
	mms.RUnlock()
	resp.Status = proto.TaskSuccess
end:
	return mms.postResponseToMaster(adminTask, resp)
}

func (mms *MockMetaServer) postResponseToMaster(adminTask *proto.AdminTask, resp interface{}) (err error) {
	adminTask.Request = nil
	adminTask.Response = resp
	data, err := json.Marshal(adminTask)
	if err != nil {
		return
	}
	_, err = PostToMaster(http.MethodPost, urlMetaNodeResponse, data)
	if err != nil {
		return
	}
	return
}

func (mms *MockMetaServer) handleDeleteMetaPartition(conn net.Conn, p *proto.Packet, adminTask *proto.AdminTask) (err error) {
	responseAckOKToMaster(conn, p)
	req := &proto.DeleteMetaPartitionRequest{}
	reqData, err := json.Marshal(adminTask.Request)
	if err != nil {
		p.PackErrorWithBody(proto.OpErr, nil)
		responseAckErrToMaster(conn, p, err)
		return
	}
	if err = json.Unmarshal(reqData, req); err != nil {
		p.PackErrorWithBody(proto.OpErr, nil)
		responseAckErrToMaster(conn, p, err)
		return
	}
	resp := &proto.DeleteMetaPartitionResponse{
		PartitionID: req.PartitionID,
		Status:      proto.TaskSuccess,
	}
	return mms.postResponseToMaster(adminTask, resp)
}

func (mms *MockMetaServer) handleUpdateMetaPartition(conn net.Conn, p *proto.Packet, adminTask *proto.AdminTask) (err error) {
	responseAckOKToMaster(conn, p)
	req := &proto.UpdateMetaPartitionRequest{}
	reqData, err := json.Marshal(adminTask.Request)
	if err != nil {
		p.PackErrorWithBody(proto.OpErr, nil)
		responseAckErrToMaster(conn, p, err)
		return
	}
	if err = json.Unmarshal(reqData, req); err != nil {
		p.PackErrorWithBody(proto.OpErr, nil)
		responseAckErrToMaster(conn, p, err)
		return
	}
	resp := &proto.UpdateMetaPartitionResponse{
		VolName:     req.VolName,
		PartitionID: req.PartitionID,
		End:         req.End,
	}
	mms.Lock()
	partition := mms.partitions[req.PartitionID]
	partition.End = req.End
	mms.Unlock()
	return mms.postResponseToMaster(adminTask, resp)
}

func (mms *MockMetaServer) handleLoadMetaPartition(conn net.Conn, p *proto.Packet, adminTask *proto.AdminTask) (err error) {
	responseAckOKToMaster(conn, p)
	req := &proto.LoadMetaPartitionMetricRequest{}
	reqData, err := json.Marshal(adminTask.Request)
	if err != nil {
		p.PackErrorWithBody(proto.OpErr, nil)
		responseAckErrToMaster(conn, p, err)
		return
	}
	if err = json.Unmarshal(reqData, req); err != nil {
		p.PackErrorWithBody(proto.OpErr, nil)
		responseAckErrToMaster(conn, p, err)
		return
	}
	resp := &proto.LoadMetaPartitionMetricResponse{
	}
	return mms.postResponseToMaster(adminTask, resp)
}

func (mms *MockMetaServer) handleOfflineMetaPartition(conn net.Conn, p *proto.Packet, adminTask *proto.AdminTask) (err error) {
	responseAckOKToMaster(conn, p)
	req := &proto.MetaPartitionOfflineRequest{}
	reqData, err := json.Marshal(adminTask.Request)
	if err != nil {
		p.PackErrorWithBody(proto.OpErr, nil)
		responseAckErrToMaster(conn, p, err)
		return
	}
	if err = json.Unmarshal(reqData, req); err != nil {
		p.PackErrorWithBody(proto.OpErr, nil)
		responseAckErrToMaster(conn, p, err)
		return
	}
	resp := &proto.MetaPartitionOfflineResponse{
	}
	return mms.postResponseToMaster(adminTask, resp)
}
