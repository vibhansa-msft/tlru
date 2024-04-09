package tlru

import (
	"container/list"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

type tlruTestSuite struct {
	suite.Suite
	assert *assert.Assertions
}

func TestTLRU(t *testing.T) {
	suite.Run(t, new(tlruTestSuite))
}

func (suite *tlruTestSuite) SetupTest() {
	suite.assert = assert.New(suite.T())
}

func (suite *tlruTestSuite) TestTLRUCreateFail() {
	tlru, err := New(0, 0, nil, 0, nil)
	suite.assert.NotNil(err)
	suite.assert.Nil(tlru)

	tlru, err = New(0, 1, nil, 0, nil)
	suite.assert.NotNil(err)
	suite.assert.Nil(tlru)
}

func (suite *tlruTestSuite) TestTLRUCreatePass() {
	f := func(node *list.Element) {}

	tlru, err := New(0, 5, f, 0, nil)
	suite.assert.Nil(err)
	suite.assert.NotNil(tlru)
}

func (suite *tlruTestSuite) TestTLRUStartStop() {
	f := func(node *list.Element) {}
	tlru, err := New(0, 5, f, 0, nil)
	suite.assert.Nil(err)
	suite.assert.NotNil(tlru)

	err = tlru.Start()
	suite.assert.Nil(err)

	err = tlru.Stop()
	suite.assert.Nil(err)
}

func (suite *tlruTestSuite) TestAddAndRefresh() {
	f := func(node *list.Element) {}
	tlru, err := New(5, 20, f, 0, nil)
	suite.assert.NotNil(tlru)
	suite.assert.Nil(err)

	err = tlru.Start()
	suite.assert.Nil(err)

	n1 := tlru.Add(1)
	suite.assert.NotNil(n1)

	n2 := tlru.Add(2)
	suite.assert.NotNil(n2)

	n3 := tlru.Add(3)
	suite.assert.NotNil(n3)

	tlru.Refresh(n1)
	time.Sleep(2 * time.Second)
	suite.assert.Equal(n1, tlru.nodeList.Front())

	err = tlru.Stop()
	suite.assert.Nil(err)
}

func (suite *tlruTestSuite) TestAddAndRefreshTimeout() {
	f := func(node *list.Element) {
		suite.T().Log("Evicted node :", node.Value)
	}

	tlru, err := New(5, 1, f, 0, nil)
	suite.assert.NotNil(tlru)
	suite.assert.Nil(err)

	err = tlru.Start()
	suite.assert.Nil(err)

	n1 := tlru.Add(1)
	suite.assert.NotNil(n1)

	n2 := tlru.Add(2)
	suite.assert.NotNil(n2)

	n3 := tlru.Add(3)
	suite.assert.NotNil(n3)

	time.Sleep(2 * time.Second)
	suite.assert.Equal(tlru.marker, tlru.nodeList.Front())

	err = tlru.Stop()
	suite.assert.Nil(err)
}

func (suite *tlruTestSuite) TestAddMaxNodes() {
	count := 0
	f := func(node *list.Element) {
		count++
		suite.T().Log("Evicted node :", node.Value)
	}

	tlru, err := New(5, 10, f, 0, nil)
	suite.assert.NotNil(tlru)
	suite.assert.Nil(err)

	err = tlru.Start()
	suite.assert.Nil(err)

	for i := 0; i < 6; i++ {
		node := tlru.Add(i)
		suite.assert.NotNil(node)
	}

	err = tlru.Stop()
	suite.assert.Nil(err)

	suite.assert.Nil(tlru.nodeList.Front())
	suite.assert.Equal(count, 6)
}

func (suite *tlruTestSuite) TestAddLargeNumberOfNodes() {
	mtx := sync.Mutex{}
	count := 0
	f := func(node *list.Element) {
		mtx.Lock()
		count++
		mtx.Unlock()
		suite.T().Log("Evicted node :", node.Value)
	}

	tlru, err := New(5, 300, f, 0, nil)
	suite.assert.NotNil(tlru)
	suite.assert.Nil(err)

	err = tlru.Start()
	suite.assert.Nil(err)

	for i := 0; i < 50; i++ {
		node := tlru.Add(i)
		suite.assert.NotNil(node)
	}

	time.Sleep(100)
	suite.assert.Equal(45, count)

	err = tlru.Stop()
	suite.assert.Nil(err)

	suite.assert.Nil(tlru.nodeList.Front())
	suite.assert.Equal(50, count)
}

func (suite *tlruTestSuite) parallelPush(tlru *TLRU, wg *sync.WaitGroup) {
	defer wg.Done()
	for i := 0; i < 1000; i++ {
		node := tlru.Add(i)
		suite.assert.NotNil(node)
	}
}

func (suite *tlruTestSuite) TestMultiThreadAdd() {
	mtx := sync.Mutex{}
	count := 0
	f := func(node *list.Element) {
		mtx.Lock()
		count++
		mtx.Unlock()
		//suite.T().Log("Evicted node :", node.Value)
	}

	tlru, err := New(100, 300, f, 0, nil)
	suite.assert.NotNil(tlru)
	suite.assert.Nil(err)

	err = tlru.Start()
	suite.assert.Nil(err)

	wg := sync.WaitGroup{}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go suite.parallelPush(tlru, &wg)
	}
	wg.Wait()

	time.Sleep(20)
	suite.assert.Equal(count, 5000-100)

	err = tlru.Stop()
	suite.assert.Nil(err)

	suite.assert.Nil(tlru.nodeList.Front())
	suite.assert.Equal(count, 5000)
}

func (suite *tlruTestSuite) testAppCheck() {
	mtx := sync.Mutex{}
	count := 0
	acount := 0
	f := func(node *list.Element) {
		mtx.Lock()
		count++
		mtx.Unlock()
		suite.T().Log("Evicted node :", node.Value)
	}

	a := func() bool {
		mtx.Lock()
		acount++
		mtx.Unlock()
		return acount < 10
	}

	tlru, err := New(20, 300, f, 2, a)
	suite.assert.NotNil(tlru)
	suite.assert.Nil(err)

	err = tlru.Start()
	suite.assert.Nil(err)

	for i := 0; i < 10; i++ {
		node := tlru.Add(i)
		suite.assert.NotNil(node)
	}

	time.Sleep(5)
	suite.assert.Equal(10, count)

	err = tlru.Stop()
	suite.assert.Nil(err)

	suite.assert.Nil(tlru.nodeList.Front())
}

func (suite *tlruTestSuite) TestAsyncEvict() {
	count := 0
	f := func(node *list.Element) {
		count++
		fmt.Println("Evicted node :", node.Value)
		suite.T().Log("Evicted node :", node.Value)
	}

	tlru, err := New(5, 3, f, 0, nil)
	suite.assert.NotNil(tlru)
	suite.assert.Nil(err)

	err = tlru.Start()
	suite.assert.Nil(err)

	for i := 0; i < 10; i++ {
		node := tlru.Add(i)
		suite.assert.NotNil(node)
	}

	time.Sleep(10)
	for i := 100; i < 105; i++ {
		node := tlru.Add(i)
		suite.assert.NotNil(node)
	}

	err = tlru.Stop()
	suite.assert.Nil(err)

	suite.assert.Nil(tlru.nodeList.Front())
	suite.assert.Equal(count, 15)
}
