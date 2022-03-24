package main

import (
	"log"
	"sync"

	"github.com/hashicorp/nomad/api"
)

type allocations struct {
	m *sync.Map
}

func (a *allocations) Add(alloc *api.Allocation) {
	log.Printf("Add alloc %s %s\n", alloc.ID, alloc.Name)
	a.m.Store(alloc.ID, alloc)
}

func (a *allocations) Del(alloc *api.Allocation) {
	log.Printf("Del alloc %s %s\n", alloc.ID, alloc.Name)
	a.m.Delete(alloc.ID)
}

func (a *allocations) Each(f func(string, *api.Allocation)) {
	a.m.Range(func(key interface{}, value interface{}) bool {
		f(key.(string), value.(*api.Allocation))
		return true
	})
}
