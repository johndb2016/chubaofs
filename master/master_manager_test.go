package master

import (
	"fmt"
	"github.com/chubaofs/chubaofs/raftstore"
	"github.com/tiglabs/raft/proto"
	"io/ioutil"
	"net/http"
	"testing"
)

func TestHandleLeaderChange(t *testing.T) {
	leaderID := server.id
	newLeaderID := leaderID + 1
	server.handleLeaderChange(newLeaderID)
	if server.metaReady != false {
		t.Errorf("logic error,metaReady should be false,metaReady[%v]", server.metaReady)
		return
	}
	server.handleLeaderChange(leaderID)
	if server.metaReady == false {
		t.Errorf("logic error,metaReady should be true,metaReady[%v]", server.metaReady)
		return
	}
}

func TestHandlerPeerChange(t *testing.T) {
	addPeerTest(t)
	removePeerTest(t)
}

func addPeerTest(t *testing.T) {
	confChange := &proto.ConfChange{
		Type:    proto.ConfAddNode,
		Peer:    proto.Peer{ID: 2},
		Context: []byte("127.0.0.2:9090"),
	}
	if err := server.handlePeerChange(confChange); err != nil {
		t.Error(err)
		return
	}
}

func removePeerTest(t *testing.T) {
	confChange := &proto.ConfChange{
		Type:    proto.ConfRemoveNode,
		Peer:    proto.Peer{ID: 2},
		Context: []byte("127.0.0.2:9090"),
	}
	if err := server.handlePeerChange(confChange); err != nil {
		t.Error(err)
		return
	}
}

func TestRaft(t *testing.T) {
	addRaftServerTest("127.0.0.1:9001", 2, t)
	removeRaftServerTest("127.0.0.1:9001", 2, t)
	snapshotTest(t)
}

func snapshotTest(t *testing.T) {
	mdSnapshot, err := server.cluster.fsm.Snapshot()
	if err != nil {
		t.Error(err)
		return
	}
	t.Logf("snapshot apply index[%v]\n", mdSnapshot.ApplyIndex())
	s := &Master{}
	dbStore := raftstore.NewRocksDBStore("/export/chubaofs/raft2")
	fsm := &MetadataFsm{
		rs:    server.fsm.rs,
		store: dbStore,
	}
	fsm.RegisterApplySnapshotHandler(func() {
		fsm.restore()
	})
	s.fsm = fsm
	peers := make([]proto.Peer, 0, len(server.config.peers))
	for _, peer := range server.config.peers {
		peers = append(peers, peer.Peer)
	}
	if err = fsm.ApplySnapshot(peers, mdSnapshot); err != nil {
		t.Error(err)
		return
	}
	if fsm.applied != mdSnapshot.ApplyIndex() {
		t.Errorf("applied not equal,applied[%v],snapshot applied[%v]\n", fsm.applied, mdSnapshot.ApplyIndex())
		return
	}
	mdSnapshot.Close()
}

func addRaftServerTest(addRaftAddr string, id uint64, t *testing.T) {
	//don't pass id test
	reqURL := fmt.Sprintf("%v%v?id=&addr=%v", hostAddr, RaftNodeAdd, addRaftAddr)
	fmt.Println(reqURL)
	resp, err := http.Get(reqURL)
	if err != nil {
		t.Errorf("err is %v", err)
		return
	}
	fmt.Println(resp.StatusCode)
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Errorf("err is %v", err)
		return
	}
	fmt.Println(string(body))
}

func removeRaftServerTest(removeRaftAddr string, id uint64, t *testing.T) {
	reqURL := fmt.Sprintf("%v%v?id=%v&addr=%v", hostAddr, RaftNodeRemove, id, removeRaftAddr)
	fmt.Println(reqURL)
	process(reqURL, t)
}
