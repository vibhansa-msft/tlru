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

	// Number of nodes allowed in the list
	MaxNodes uint32

	// Function called back for eviceted nodes
	Evict func(*list.Element)

	// Expiry timer
	expiry <-chan time.Time

	// Channle to hold list of nodes to be refreshed
	refresh chan *list.Element

	// Current number of nodes in the list
	nodes uint32

	// Acutal linked list to hold the nodes
	nodeList *list.List

	// Marker node to keep track of expired nodes
	marker *list.Element

	// Channel to mark user has requested to stop the TLRU
	done chan int
}

// NewTLRU : Create a new Time based LRU
func New(maxNodes, timeout uint32, evict func(*list.Element)) (*TLRU, error) {
	if timeout == 0 {
		return nil, fmt.Errorf("timeout can not be zero")
	}

	if evict == nil {
		return nil, fmt.Errorf("no eviction method provided")
	}

	return &TLRU{
		MaxNodes: maxNodes,
		Timeout:  timeout,
		Evict:    evict,
		nodes:    0,
		nodeList: list.New(),
	}, nil
}

// Start : Init the LRU and start the watchDog thread
func (tlru *TLRU) Start() error {
	// Create ticker for timeout
	tlru.expiry = time.Tick(time.Duration(time.Duration(tlru.Timeout) * time.Second))

	// Channel to hold refresh backlog
	tlru.refresh = make(chan *list.Element, 10000)

	// Marker node to handle timeout and mark expired nodes
	tlru.marker = tlru.nodeList.PushFront("###")

	// Create channel to mark the completion from user application
	tlru.done = make(chan int, 1)

	tlru.wg.Add(1)
	go tlru.watchDog()

	return nil
}

// Stop : Stop the watchdog thread and expire all nodes
func (tlru *TLRU) Stop() error {
	tlru.done <- 1
	tlru.wg.Wait()

	close(tlru.refresh)
	tlru.refresh = nil

	tlru.nodeList.Remove(tlru.marker)
	tlru.marker = nil

	return nil
}

// Add : Add this new node to the lru
func (tlru *TLRU) Add(data any) *list.Element {
	tlru.Lock()
	defer tlru.Unlock()

	// Create a new node and push it to the front of linked list
	node := tlru.nodeList.PushFront(data)
	tlru.nodes++

	if tlru.MaxNodes != 0 && tlru.nodes > tlru.MaxNodes {
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
	tlru.removeInternal(node)
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

		case node := <-tlru.refresh:
			// bringToFront this node to front of the lru
			tlru.Lock()
			tlru.nodeList.MoveToFront(node)
			tlru.Unlock()

		case <-tlru.done:
			for len(tlru.refresh) > 0 {
				<-tlru.refresh
			}

			tlru.updateMarker()
			_ = tlru.expireNodes()
			return
		}
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
