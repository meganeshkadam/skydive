/*
 * Copyright (C) 2016 Red Hat, Inc.
 *
 * Licensed to the Apache Software Foundation (ASF) under one
 * or more contributor license agreements.  See the NOTICE file
 * distributed with this work for additional information
 * regarding copyright ownership.  The ASF licenses this file
 * to you under the Apache License, Version 2.0 (the
 * "License"); you may not use this file except in compliance
 * with the License.  You may obtain a copy of the License at
 *
 *  http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 *
 */

package client

import (
	"sync"

	"github.com/skydive-project/skydive/api"
	"github.com/skydive-project/skydive/common"
	"github.com/skydive-project/skydive/etcd"
	"github.com/skydive-project/skydive/flow/ondemand"
	shttp "github.com/skydive-project/skydive/http"
	"github.com/skydive-project/skydive/logging"
	"github.com/skydive-project/skydive/topology"
	"github.com/skydive-project/skydive/topology/graph"
)

type OnDemandProbeClient struct {
	sync.RWMutex
	graph.DefaultGraphListener
	graph          *graph.Graph
	captureHandler *api.CaptureAPIHandler
	wsServer       *shttp.WSServer
	captures       map[string]*api.Capture
	watcher        api.StoppableWatcher
	elector        *etcd.EtcdMasterElector
}

func (o *OnDemandProbeClient) registerProbes(nodes []interface{}, capture *api.Capture) {
	for _, i := range nodes {
		switch i.(type) {
		case *graph.Node:
			o.graph.RLock()
			node := i.(*graph.Node)
			if _, err := node.GetFieldString("Capture/ID"); err == nil {
				o.graph.RUnlock()
				return
			}
			nodeID := node.ID
			host := node.Host()
			o.graph.RUnlock()
			o.registerProbe(nodeID, host, capture)
		case []*graph.Node:
			// case of shortestpath that return a list of nodes
			for _, node := range i.([]*graph.Node) {
				o.graph.RLock()
				if _, err := node.GetFieldString("Capture/ID"); err == nil {
					o.graph.RUnlock()
					return
				}
				nodeID := node.ID
				host := node.Host()
				o.graph.RUnlock()
				o.registerProbe(nodeID, host, capture)
			}
		}
	}
}

func (o *OnDemandProbeClient) registerProbe(id graph.Identifier, host string, capture *api.Capture) bool {
	cq := ondemand.CaptureQuery{
		NodeID:  string(id),
		Capture: *capture,
	}

	msg := shttp.NewWSMessage(ondemand.Namespace, "CaptureStart", cq)

	if !o.wsServer.SendWSMessageTo(msg, host) {
		logging.GetLogger().Errorf("Unable to send message to agent: %s", host)
		return false
	}
	return true
}

func (o *OnDemandProbeClient) unregisterProbe(node *graph.Node) bool {
	msg := shttp.NewWSMessage(ondemand.Namespace, "CaptureStop", ondemand.CaptureQuery{NodeID: string(node.ID)})

	if !o.wsServer.SendWSMessageTo(msg, node.Host()) {
		logging.GetLogger().Errorf("Unable to send message to agent: %s", node.Host())
		return false
	}

	return true
}

func (o *OnDemandProbeClient) applyGremlinExpr(query string) []interface{} {
	res, err := topology.ExecuteGremlinQuery(o.graph, query)
	if err != nil {
		logging.GetLogger().Errorf("Gremlin error: %s", err.Error())
		return nil
	}
	return res.Values()
}

func (o *OnDemandProbeClient) onNodeEvent() {
	if !o.elector.IsMaster() {
		return
	}

	for _, capture := range o.captures {
		res := o.applyGremlinExpr(capture.GremlinQuery)
		if len(res) > 0 {
			go o.registerProbes(res, capture)
		}
	}
}

func (o *OnDemandProbeClient) OnNodeAdded(n *graph.Node) {
	o.onNodeEvent()
}

func (o *OnDemandProbeClient) OnNodeUpdated(n *graph.Node) {
	o.onNodeEvent()
}

func (o *OnDemandProbeClient) OnEdgeAdded(e *graph.Edge) {
	o.onNodeEvent()
}

func (o *OnDemandProbeClient) onCaptureAdded(capture *api.Capture) {
	if !o.elector.IsMaster() {
		return
	}

	o.Lock()
	defer o.Unlock()

	o.graph.RLock()
	defer o.graph.RUnlock()

	o.captures[capture.UUID] = capture

	nodes := o.applyGremlinExpr(capture.GremlinQuery)
	if len(nodes) > 0 {
		go o.registerProbes(nodes, capture)
	}
}

func (o *OnDemandProbeClient) onCaptureDeleted(capture *api.Capture) {
	if !o.elector.IsMaster() {
		return
	}

	o.Lock()
	defer o.Unlock()

	o.graph.Lock()
	defer o.graph.Unlock()

	delete(o.captures, capture.UUID)

	res, err := topology.ExecuteGremlinQuery(o.graph, capture.GremlinQuery)
	if err != nil {
		logging.GetLogger().Errorf("Gremlin error: %s", err.Error())
		return
	}

	for _, value := range res.Values() {
		switch e := value.(type) {
		case *graph.Node:
			if !o.unregisterProbe(e) {
				logging.GetLogger().Errorf("Failed to stop capture on %s", e.ID)
			}
		case []*graph.Node:
			for _, node := range e {
				if !o.unregisterProbe(node) {
					logging.GetLogger().Errorf("Failed to stop capture on %s", node.ID)
				}
			}
		default:
			return
		}
	}
}

func (o *OnDemandProbeClient) onAPIWatcherEvent(action string, id string, resource api.APIResource) {
	logging.GetLogger().Debugf("New watcher event %s for %s", action, id)
	capture := resource.(*api.Capture)
	switch action {
	case "init", "create", "set", "update":
		o.wsServer.BroadcastWSMessage(shttp.NewWSMessage(ondemand.Namespace, "CaptureAdded", capture))
		o.onCaptureAdded(capture)
	case "expire", "delete":
		o.wsServer.BroadcastWSMessage(shttp.NewWSMessage(ondemand.Namespace, "CaptureDeleted", capture))
		o.onCaptureDeleted(capture)
	}
}

func (o *OnDemandProbeClient) Start() {
	o.elector.StartAndWait()

	o.watcher = o.captureHandler.AsyncWatch(o.onAPIWatcherEvent)
	o.graph.AddEventListener(o)
}

func (o *OnDemandProbeClient) Stop() {
	o.watcher.Stop()
	o.elector.Stop()
}

func NewOnDemandProbeClient(g *graph.Graph, ch *api.CaptureAPIHandler, w *shttp.WSServer, etcdClient *etcd.EtcdClient) *OnDemandProbeClient {
	resources := ch.Index()
	captures := make(map[string]*api.Capture)
	for _, resource := range resources {
		captures[resource.ID()] = resource.(*api.Capture)
	}

	elector := etcd.NewEtcdMasterElectorFromConfig(common.AnalyzerService, "ondemand-client", etcdClient)

	return &OnDemandProbeClient{
		graph:          g,
		captureHandler: ch,
		wsServer:       w,
		captures:       captures,
		elector:        elector,
	}
}
