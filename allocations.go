package main

import (
	"log"
	"sync"

	"github.com/hashicorp/nomad/api"
)

type allocations struct {
	allocs map[string]*api.Allocation
	lock   *sync.RWMutex
}

func (a *allocations) Add(alloc *api.Allocation) {
	a.lock.Lock()
	defer a.lock.Unlock()
	log.Println("Adding alloc", alloc.ID)
	a.allocs[alloc.ID] = alloc
}

func (a *allocations) Del(alloc *api.Allocation) {
	a.lock.Lock()
	defer a.lock.Unlock()
	log.Println("Removing alloc", alloc.ID)
	delete(a.allocs, alloc.ID)
}

func (a *allocations) Each(f func(string, *api.Allocation)) {
	a.lock.RLock()
	defer a.lock.RUnlock()
	for id, alloc := range a.allocs {
		f(id, alloc)
	}
}
