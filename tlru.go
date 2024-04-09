package tlru

import (
	"container/list"
	"fmt"
	"sync"
	"time"
)

type TLRU struct {
	// Lock for the TLRU
	sync.Mutex

	// Wait for the watchdog thread
	wg sync.WaitGroup

	// Timeout to evict the nodes which are not used for given interval
	Timeout uint32

	// Timeout to callback application to check if it wants to evict last node
	AppCheckTimeout uint32

	// Number of nodes allowed in the list
	MaxNodes uint32

	// Function called back for eviceted nodes
	Evict func(*list.Element)

	// Function to check if application wants to force evict last node
	AppCheck func() bool

	// Expiry timer
	expiry <-chan time.Time

	// App Expiry timer
	appExpiry <-chan time.Time

	// Channle to hold list of nodes to be refreshed
	refresh chan *list.Element

	// Chnnel to hold files to be deleted
	evictList chan *list.Element

	// Current number of nodes in the list
	nodes uint32

	// Acutal linked list to hold the nodes
	nodeList *list.List

	// Marker node to keep track of expired nodes
	marker *list.Element

	// Channel to mark user has requested to stop the TLRU
	done chan int

	evictMode func(node *list.Element)
}

// NewTLRU : Create a new Time based LRU
func New(maxNodes, timeout uint32, evict func(*list.Element), appchecktimeout uint32, appcheck func() bool) (*TLRU, error) {
	if timeout == 0 {
		return nil, fmt.Errorf("timeout can not be zero")
	}

	if evict == nil {
		return nil, fmt.Errorf("no eviction method provided")
	}

	return &TLRU{
		MaxNodes:        maxNodes,
		Timeout:         timeout,
		Evict:           evict,
		AppCheckTimeout: appchecktimeout,
		AppCheck:        appcheck,
		nodes:           0,
		nodeList:        list.New(),
		evictMode:       nil,
	}, nil
}

// Start : Init the LRU and start the watchDog thread
func (tlru *TLRU) Start() error {
	// Create ticker for timeout
	if tlru.Timeout > 0 {
		tlru.expiry = time.Tick(time.Duration(time.Duration(tlru.Timeout) * time.Second))
	}

	// Create ticker for timeout
	if tlru.AppCheckTimeout > 0 {
		tlru.appExpiry = time.Tick(time.Duration(time.Duration(tlru.AppCheckTimeout) * time.Second))
	}

	// Channel to hold refresh backlog
	tlru.refresh = make(chan *list.Element, 10000)

	// Channel to hold async delete backlog
	tlru.evictList = make(chan *list.Element, tlru.MaxNodes)

	// Marker node to handle timeout and mark expired nodes
	tlru.marker = tlru.nodeList.PushFront("###")

	// Create channel to mark the completion from user application
	tlru.done = make(chan int, 1)

	tlru.evictMode = tlru.evictAsync

	tlru.wg.Add(1)
	go tlru.watchDog()

	// At time of stop we will close the channel and then wait for this thread to complete
	// so wg.add will be done at Stop time.
	go tlru.asyncEviction()

	return nil
}

// Stop : Stop the watchdog thread and expire all nodes
func (tlru *TLRU) Stop() error {
	tlru.done <- 1
	tlru.wg.Wait()

	close(tlru.refresh)
	tlru.refresh = nil

	tlru.wg.Add(1)
	close(tlru.evictList)
	tlru.wg.Wait()
	tlru.evictList = nil

	tlru.nodeList.Remove(tlru.marker)
	tlru.marker = nil

	return nil
}

// Add : Add this new node to the lru
func (tlru *TLRU) Add(data any) *list.Element {
	expire := false

	tlru.Lock()
	// Create a new node and push it to the front of linked list
	node := tlru.nodeList.PushFront(data)
	tlru.nodes++

	if tlru.MaxNodes != 0 && tlru.nodes > tlru.MaxNodes {
		expire = true
	}
	tlru.Unlock()

	if expire {
		// List has grown so time to expire atleast one node
		go tlru.expireNode()
	}

	return node
}

// expireNode : Expire the last node
func (tlru *TLRU) expireNode() {
	if tlru.nodeList == nil {
		return
	}

	tlru.Lock()
	defer tlru.Unlock()

	// Expire the last node from the list
	node := tlru.nodeList.Back()
	if node == tlru.marker {
		// Lst node is marker so we need to skip that
		node = node.Prev()
	}

	// Remove this node from list and call evict
	if node != nil {
		tlru.removeInternal(node)
	}

	return
}

// Remove : Delete this new node from the lru
func (tlru *TLRU) Remove(node *list.Element) {
	tlru.Lock()
	defer tlru.Unlock()

	// Remove the node and call evict
	tlru.removeInternal(node)
}

// removeInternal : actual code to remove a node form the list
func (tlru *TLRU) removeInternal(node *list.Element) {
	tlru.nodeList.Remove(node)
	tlru.nodes--
	tlru.evictMode(node)
}

// evictAsync : evict the expired node in async mode
func (tlru *TLRU) evictAsync(node *list.Element) {
	tlru.evictList <- node
}

// evictAsync : evict the expired node in sync mode
func (tlru *TLRU) evictSync(node *list.Element) {
	tlru.Evict(node)
}

// RefreshNode : Request to mark this node as used
func (tlru *TLRU) Refresh(node *list.Element) {
	// Watchdog thread will take care of moving this node to front of the list
	tlru.refresh <- node
}

// watchDog : Thread monitoring for timeouts and expiry
func (tlru *TLRU) watchDog() {
	defer tlru.wg.Done()

	for {
		select {
		case <-tlru.expiry:
			// Time to expire the unused nodes
			tlru.expireNodes()
			tlru.updateMarker()

		case <-tlru.appExpiry:
			// Time to check with application if it wants to expire last node
			/* This is useful in cases where application based on some logic wishes to evict
			   a node early, instead of waiting for the timeout
			*/
			if tlru.AppCheck() {
				tlru.expireNode()
			}

		case node := <-tlru.refresh:
			// bringToFront this node to front of the lru
			tlru.Lock()
			tlru.nodeList.MoveToFront(node)
			tlru.Unlock()

		case <-tlru.done:
			for len(tlru.refresh) > 0 {
				<-tlru.refresh
			}

			tlru.evictMode = tlru.evictSync
			tlru.updateMarker()
			_ = tlru.expireNodes()
			return
		}
	}
}

// asyncEviction : Thread to evict nodes in async mode
func (tlru *TLRU) asyncEviction() {
	defer tlru.wg.Done()

	for {
		node, ok := <-tlru.evictList
		if !ok {
			// Channel is closed time to exit
			return
		}
		tlru.Evict(node)
	}
}

// updateMarker : Update the marker and set them to expire nodes
func (tlru *TLRU) updateMarker() {
	// If marker is already at head then nothing to be done
	if tlru.nodeList.Front() == tlru.marker {
		return
	}

	tlru.Lock()
	defer tlru.Unlock()
	tlru.nodeList.MoveToFront(tlru.marker)
}

// expireNodes : Expire the Least recently used nodes and send callback to application
func (tlru *TLRU) expireNodes() uint32 {
	// If marker is already at head then nothing to be done
	if tlru.nodeList.Back() == tlru.marker {
		return 0
	}

	count := uint32(0)
	for {
		tlru.Lock()
		node := tlru.nodeList.Back()
		if node == tlru.marker {
			tlru.Unlock()
			break
		}

		tlru.removeInternal(node)
		tlru.Unlock()

		count++
	}

	return count
}
