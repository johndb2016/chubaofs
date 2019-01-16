// Modified work copyright (C) 2018 The CFS Authors.
//
// The MIT License (MIT)
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package pool

import (
	"net"
	"strings"
	"sync"
	"time"
)

type Object struct {
	conn *net.TCPConn
	idle int64
}

type Pool struct {
	pool    chan *Object
	mincap  int
	maxcap  int
	target  string
	timeout int64
}

func NewPool(min, max int, timeout int64, target string) (p *Pool) {
	p = new(Pool)
	p.mincap = min
	p.maxcap = max
	p.target = target
	p.pool = make(chan *Object, max)
	p.timeout = timeout
	p.initAllConnect()
	return p
}

func (p *Pool) initAllConnect() {
	for i := 0; i < p.mincap; i++ {
		c, err := net.Dial("tcp", p.target)
		if err == nil {
			conn := c.(*net.TCPConn)
			conn.SetKeepAlive(true)
			conn.SetNoDelay(true)
			obj := &Object{conn: conn, idle: time.Now().UnixNano()}
			p.putconnect(obj)
		}
	}
}

func (p *Pool) putconnect(o *Object) {
	select {
	case p.pool <- o:
		return
	default:
		if o.conn != nil {
			o.conn.Close()
		}
		return
	}
}

func (p *Pool) getconnect() (o *Object) {
	select {
	case o = <-p.pool:
		return
	default:
		return
	}
}

func (p *Pool) AutoRelease() {
	connectLen := len(p.pool)
	for i := 0; i < connectLen; i++ {
		select {
		case o := <-p.pool:
			if time.Now().UnixNano()-int64(o.idle) > p.timeout {
				o.conn.Close()
			} else {
				p.putconnect(o)
			}
		default:
			return
		}
	}
}

func (p *Pool) ForceReleaseAllConnect() {
	for {
		select {
		case o := <-p.pool:
			o.conn.Close()
		default:
			return
		}
	}
}

func (p *Pool) Get() (c *net.TCPConn, err error) {
	obj := p.getconnect()
	if obj != nil {
		return obj.conn, nil
	}
	var connect net.Conn
	connect, err = net.Dial("tcp", p.target)
	if err == nil {
		conn := connect.(*net.TCPConn)
		conn.SetKeepAlive(true)
		conn.SetNoDelay(true)
		c = conn
	}
	return
}

type ConnectPool struct {
	sync.RWMutex
	pools   map[string]*Pool
	mincap  int
	maxcap  int
	timeout int64
}

func NewConnectPool() (cp *ConnectPool) {
	cp = &ConnectPool{pools: make(map[string]*Pool), mincap: 5, maxcap: 100, timeout: int64(time.Minute)}
	go cp.autoRelease()

	return cp
}

func (cp *ConnectPool) Get(targetAddr string) (c *net.TCPConn, err error) {
	cp.RLock()
	pool, ok := cp.pools[targetAddr]
	cp.RUnlock()
	if !ok {
		cp.Lock()
		pool = NewPool(cp.mincap, cp.maxcap, cp.timeout, targetAddr)
		cp.pools[targetAddr] = pool
		cp.Unlock()
	}

	return pool.Get()
}

func (cp *ConnectPool) Put(c *net.TCPConn, forceClose bool) {
	if c == nil {
		return
	}
	if forceClose {
		c.Close()
		return
	}
	addr := c.RemoteAddr().String()
	cp.RLock()
	pool, ok := cp.pools[addr]
	cp.RUnlock()
	if !ok {
		c.Close()
		return
	}
	object := &Object{conn: c, idle: time.Now().UnixNano()}
	pool.putconnect(object)

	return
}

func (cp *ConnectPool) CheckErrorForceClose(c *net.TCPConn, target string, err error) {
	if c == nil {
		return
	}

	if err != nil {
		if strings.Contains(err.Error(), "use of closed network connection") {
			c.Close()
			cp.ReleaseAllConnect(target)
			return
		} else {
			c.Close()
			return
		}
	}
}

func (cp *ConnectPool) CheckErrorForPutConnect(c *net.TCPConn, target string, err error) {
	if c == nil {
		return
	}

	if err != nil {
		if strings.Contains(err.Error(), "use of closed network connection") {
			c.Close()
			cp.ReleaseAllConnect(target)
			return
		} else {
			c.Close()
			return
		}
	}
	addr := c.RemoteAddr().String()
	cp.RLock()
	pool, ok := cp.pools[addr]
	cp.RUnlock()
	if !ok {
		c.CloseWrite()
		c.CloseRead()
		c.Close()
		return
	}
	object := &Object{conn: c, idle: time.Now().UnixNano()}
	pool.putconnect(object)
}

func (cp *ConnectPool) ReleaseAllConnect(target string) {
	cp.RLock()
	pool := cp.pools[target]
	cp.RUnlock()
	if pool != nil {
		pool.ForceReleaseAllConnect()
	}
}

func (cp *ConnectPool) autoRelease() {
	for {
		pools := make([]*Pool, 0)
		cp.RLock()
		for _, pool := range cp.pools {
			pools = append(pools, pool)
		}
		cp.RUnlock()
		for _, pool := range pools {
			pool.AutoRelease()
		}
		time.Sleep(time.Minute)
	}

}
