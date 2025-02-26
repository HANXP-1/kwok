/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"encoding/binary"
	"net"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/labels"
)

func parseCIDR(s string) (*net.IPNet, error) {
	ip, ipnet, err := net.ParseCIDR(s)
	if err != nil {
		return nil, err
	}
	ipnet.IP = ip
	return ipnet, nil
}

func addIP(ip net.IP, add uint64) net.IP {
	if len(ip) < 8 {
		return ip
	}

	out := make(net.IP, len(ip))
	copy(out, ip)

	i := binary.BigEndian.Uint64(out[len(out)-8:])
	i += add

	binary.BigEndian.PutUint64(out[len(out)-8:], i)
	return out
}

type ipPool struct {
	mut    sync.Mutex
	used   map[string]struct{}
	usable map[string]struct{}
	cidr   *net.IPNet
	index  uint64
}

func newIPPool(cidr *net.IPNet) *ipPool {
	return &ipPool{
		used:   make(map[string]struct{}),
		usable: make(map[string]struct{}),
		cidr:   cidr,
	}
}

func (i *ipPool) new() string {
	for {
		ip := addIP(i.cidr.IP, i.index).String()
		i.index++

		if _, ok := i.used[ip]; ok {
			continue
		}

		i.used[ip] = struct{}{}
		i.usable[ip] = struct{}{}
		return ip
	}
}

func (i *ipPool) Get() string {
	i.mut.Lock()
	defer i.mut.Unlock()
	ip := ""
	if len(i.usable) != 0 {
		for s := range i.usable {
			ip = s
		}
	}
	if ip == "" {
		ip = i.new()
	}
	delete(i.usable, ip)
	i.used[ip] = struct{}{}
	return ip
}

func (i *ipPool) Put(ip string) {
	i.mut.Lock()
	defer i.mut.Unlock()
	if !i.cidr.Contains(net.ParseIP(ip)) {
		return
	}
	delete(i.used, ip)
	i.usable[ip] = struct{}{}
}

func (i *ipPool) Use(ip string) {
	i.mut.Lock()
	defer i.mut.Unlock()
	if !i.cidr.Contains(net.ParseIP(ip)) {
		return
	}
	i.used[ip] = struct{}{}
}

type parallelTasks struct {
	wg     sync.WaitGroup
	bucket chan struct{}
	tasks  chan func()
}

func newParallelTasks(n int) *parallelTasks {
	return &parallelTasks{
		bucket: make(chan struct{}, n),
		tasks:  make(chan func()),
	}
}

func (p *parallelTasks) Add(fun func()) {
	p.wg.Add(1)
	select {
	case p.tasks <- fun: // there are idle threads
	case p.bucket <- struct{}{}: // there are free threads
		go p.fork()
		p.tasks <- fun
	}
}

func (p *parallelTasks) fork() {
	defer func() {
		<-p.bucket
	}()
	timer := time.NewTimer(time.Second / 2)
	for {
		select {
		case <-timer.C: // idle threads
			return
		case fun := <-p.tasks:
			fun()
			p.wg.Done()
			timer.Reset(time.Second / 2)
		}
	}
}

func (p *parallelTasks) Wait() {
	p.wg.Wait()
}

func labelsParse(selector string) (labels.Selector, error) {
	if selector == "" {
		return nil, nil
	}
	return labels.Parse(selector)
}
