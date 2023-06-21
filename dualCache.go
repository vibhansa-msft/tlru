package tlru

import (
	"container/list"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sync"
)

type NodeState int

const (
	NODE_FREE NodeState = iota
	NODE_CACHED
	NODE_PERSISTED
	NODE_DELETED
)

type CacheNode struct {
	// Mutext to safeguard this node
	sync.RWMutex

	// Key of the node
	Key string

	// Actual data stored by user
	Value []byte

	// State of the node depicting its in cache or disk
	State NodeState
}

type MemCacheconfig struct {
	// Timeout to evict the nodes which are not used for given interval
	Timeout uint32

	// Number of nodes allowed in the list
	MaxNodes uint32

	// Function called back for eviceted nodes
	evict func(*list.Element)
}

type DiskCacheConfig struct {
	// Timeout to evict the nodes which are not used for given interval
	Timeout uint32

	// Number of nodes allowed in the list
	MaxNodes uint32

	// Path where persistent data will be stored
	DiskPath string

	// Function called back for eviceted nodes
	evict func(*list.Element)
}

// DualCacheConfig : Config params for dual-cache
type DualCacheConfig struct {
	// Config for memory based caching
	MemConfig MemCacheconfig

	// Config for disk based caching
	DiskConfig DiskCacheConfig

	// Callback Function to write data to disk
	Log func(string)
}

type DualCache struct {
	// Lock to save the map of keys
	sync.RWMutex

	// LRU to contain data stored in memory
	memCache *TLRU

	// LRU to contain data stored in disk
	diskCache *TLRU

	// Config of this dual cache
	DualCacheConfig

	nodeMap map[any]*list.Element
}

// NewDualCache : Method to create object of dual cache
func NewDualCache(cfg DualCacheConfig) (*DualCache, error) {
	// Create the object
	dualCache := new(DualCache)
	if dualCache == nil {
		return nil, nil
	}

	// Save the config
	dualCache.DualCacheConfig = cfg

	return dualCache, nil
}

// Start : Start the Cache
func (dlc *DualCache) Start() (err error) {
	// dlc.memCache, err =  New(dlc.MemConfig.MaxNodes, dlc.MemConfig.Timeout, dlc.memEvict)
	// if err != nil {
	// 	return err
	// }

	// dlc.diskCache, err =  New(dlc.DiskConfig.MaxNodes, dlc.DiskConfig.Timeout, dlc.diskEvict)
	// if err != nil {
	// 	return err
	// }

	return nil
}

// Stop : Stop the cache
func (dlc *DualCache) Stop() (err error) {
	err = dlc.memCache.Stop()
	if err != nil {
		return err
	}

	err = dlc.diskCache.Stop()
	if err != nil {
		return err
	}

	return nil
}

// memEvict : Callback for eviction from mem TLRU
func (dlc *DualCache) memEvict(node *list.Element) {
	cacheNode := node.Value.(*CacheNode)
	if cacheNode == nil {
		// Retrieved node is nil so just ignore the eviction
		return
	}

	cacheNode.Lock()
	defer cacheNode.Unlock()

	// Create a file where this node will be saved on disk
	fname := filepath.Join(dlc.DiskConfig.DiskPath, cacheNode.Key)
	err := ioutil.WriteFile(fname, cacheNode.Value, 0777)
	if err != nil {
		// Failed to dump this node to disk
		dlc.spitLog(fmt.Sprintf("failed to write node to disk [%s]", cacheNode.Key))
		dlc.deleteFromMap(cacheNode.Key)
		return
	}

	cacheNode.State = NODE_PERSISTED
	listNode := dlc.diskCache.Add(cacheNode)

	dlc.Lock()
	dlc.nodeMap[cacheNode.Key] = listNode
	dlc.Unlock()
}

// diskEvict : Callback for eviction from disk TLRU
func (dlc *DualCache) diskEvict(node *list.Element) {
	cacheNode := node.Value.(*CacheNode)
	if cacheNode == nil {
		// Retrieved node is nil so just ignore the eviction
		return
	}

	// As its eviction from disk just delete the node
	cacheNode.Lock()
	cacheNode.State = NODE_DELETED
	dlc.deleteFromMap(cacheNode.Key)
	cacheNode.Unlock()

	go func() {
		fname := filepath.Join(dlc.DiskConfig.DiskPath, cacheNode.Key)
		err := os.Remove(fname)
		if err != nil {
			// Failed to delete this file
			dlc.spitLog(fmt.Sprintf("failed to delete node from disk [%s](%s)", cacheNode.Key, err.Error()))
		}
	}()
}

// Get : Get a node from the cache
func (dlc *DualCache) Get(key string) []byte {
	var cacheNode *CacheNode
	cacheNode = nil

	dlc.RLock()
	listNode, exists := dlc.nodeMap[key]
	dlc.RUnlock()

	if exists {
		cacheNode = listNode.Value.(*CacheNode)
		cacheNode.Lock()
		defer cacheNode.Unlock()

		if cacheNode.State == NODE_CACHED {
			// Node already in cache so just a refresh is required
			dlc.memCache.Refresh(listNode)
			return cacheNode.Value
		} else if cacheNode.State == NODE_PERSISTED {
			// Node is persisted on disk so we need to bring it back
			dlc.diskCache.Remove(listNode)
			listNode := dlc.memCache.Add(cacheNode)

			// Add new node to map
			dlc.Lock()
			dlc.nodeMap[key] = listNode
			dlc.Unlock()

			var err error
			fname := filepath.Join(dlc.DiskConfig.DiskPath, cacheNode.Key)
			cacheNode.Value, err = ioutil.ReadFile(fname)
			if err != nil {
				dlc.spitLog(fmt.Sprintf("failed to read from disk [%s](%s)", cacheNode.Key, err.Error()))
				return nil
			}
		} else if cacheNode.State == NODE_DELETED {
			// Node is already deleted so treat it as a new case now
			listNode = nil
		} else {
			// Invalid case
			dlc.spitLog(fmt.Sprintf("invalide node state [%s]", cacheNode.Key))
			listNode = nil
		}
	}

	return nil
}

// Add : Add a new node to the cache
func (dlc *DualCache) Add(key string, value []byte) error {
	var cacheNode *CacheNode
	cacheNode = nil

	dlc.Lock()
	defer dlc.Unlock()

	listNode, exists := dlc.nodeMap[key]

	if exists {
		// Node already added to the map
		return nil
	}

	cacheNode = new(CacheNode)
	cacheNode.Lock()
	defer cacheNode.Unlock()

	cacheNode.State = NODE_CACHED
	cacheNode.Key = key
	cacheNode.Value = value

	listNode = dlc.memCache.Add(listNode)

	dlc.Lock()
	dlc.nodeMap[key] = listNode
	dlc.Unlock()

	return nil
}

// Stop : Stop the cache
func (dlc *DualCache) Remove(key string) error {
	dlc.Lock()
	defer dlc.Unlock()

	listNode, exists := dlc.nodeMap[key]

	if !exists {
		// Node already added to the map
		return nil
	}

	cacheNode := listNode.Value.(*CacheNode)
	cacheNode.Lock()
	defer cacheNode.Unlock()

	if cacheNode.State == NODE_CACHED {
		// Node already in cache so just a refresh is required
		dlc.memCache.Remove(listNode)
	} else if cacheNode.State == NODE_PERSISTED {
		// Node is persisted on disk so we need to bring it back
		dlc.diskEvict(listNode)
	}

	delete(dlc.nodeMap, key)

	return nil
}

// -------------------------------------------------------------------------------------------------------------
// Utils methods required by this lib

// spitLog : Function to split the log using callback method provided by user
func (dlc *DualCache) spitLog(line string) {
	if dlc.Log != nil {
		dlc.Log(line)
	}
}

// deleteFromMap : Method to sync-delete an entry from the map
func (dlc *DualCache) deleteFromMap(key string) {
	dlc.Lock()
	delete(dlc.nodeMap, key)
	dlc.Unlock()
}

// -------------------------------------------------------------------------------------------------------------
