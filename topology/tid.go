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

package topology

import (
	"github.com/nu7hatch/gouuid"

	"github.com/skydive-project/skydive/logging"
	"github.com/skydive-project/skydive/topology/graph"
)

type TIDMapper struct {
	graph.DefaultGraphListener
	Graph  *graph.Graph
	hostID graph.Identifier
}

func (t *TIDMapper) Start() {
	t.Graph.AddEventListener(t)
}

func (t *TIDMapper) Stop() {
	t.Graph.RemoveEventListener(t)
}

func (t *TIDMapper) setTID(parent, child *graph.Node) {
	tp, _ := child.GetFieldString("Type")
	if tp == "" {
		return
	}

	name, _ := child.GetFieldString("Name")
	if name == "" {
		return
	}

	if tid, _ := parent.GetFieldString("TID"); tid != "" {
		tid = tid + name + tp
		u, _ := uuid.NewV5(uuid.NamespaceOID, []byte(tid))
		t.Graph.AddMetadata(child, "TID", u.String())
	}
}

func (t *TIDMapper) setChildrenTID(parent *graph.Node) {
	children := t.Graph.LookupChildren(parent, graph.Metadata{}, graph.Metadata{"RelationType": "ownership"})
	for _, child := range children {
		t.setTID(parent, child)
	}
}

// onNodeEvent set TID
// TID is UUIDV5(ID/UUID) of "root" node like host, netns, ovsport, fabric
// for other nodes TID is UUIDV5(rootTID + Name + Type)
func (t *TIDMapper) onNodeEvent(n *graph.Node) {
	if _, err := n.GetFieldString("TID"); err != nil {
		if tp, err := n.GetFieldString("Type"); err == nil {
			switch tp {
			case "host":
				t.hostID = n.ID
				t.Graph.AddMetadata(n, "TID", string(n.ID))

				t.setChildrenTID(n)
			case "netns":
				if path, _ := n.GetFieldString("Path"); path != "" {
					tid := string(t.hostID) + path + tp
					u, _ := uuid.NewV5(uuid.NamespaceOID, []byte(tid))
					t.Graph.AddMetadata(n, "TID", u.String())

					t.setChildrenTID(n)
				}
			case "ovsport":
				if u, _ := n.GetFieldString("UUID"); u != "" {
					tid := string(t.hostID) + u + tp
					u, _ := uuid.NewV5(uuid.NamespaceOID, []byte(tid))
					t.Graph.AddMetadata(n, "TID", u.String())

					t.setChildrenTID(n)
				}
			default:
				if probe, _ := n.GetFieldString("Probe"); probe == "fabric" {
					t.Graph.AddMetadata(n, "TID", string(n.ID))
				} else {
					parents := t.Graph.LookupParents(n, graph.Metadata{}, graph.Metadata{"RelationType": "ownership"})
					if len(parents) > 1 {
						logging.GetLogger().Errorf("A should always only have one ownership parent: %v", n)
					} else if len(parents) == 1 {
						t.setTID(parents[0], n)
					}
				}
			}
		}
	}
}

func (t *TIDMapper) OnNodeUpdated(n *graph.Node) {
	t.onNodeEvent(n)
}

func (t *TIDMapper) OnNodeAdded(n *graph.Node) {
	t.onNodeEvent(n)
}

// onEdgeEvent set TID for child TID nodes which is composed of the name
// the TID of the parent node and the type.
func (t *TIDMapper) onEdgeEvent(e *graph.Edge) {
	if rl, _ := e.GetFieldString("RelationType"); rl != "ownership" {
		return
	}

	parents, children := t.Graph.GetEdgeNodes(e, graph.Metadata{}, graph.Metadata{})
	if len(parents) == 0 || len(children) == 0 {
		return
	}

	t.setTID(parents[0], children[0])
}

func (t *TIDMapper) OnEdgeUpdated(e *graph.Edge) {
	t.onEdgeEvent(e)
}

func (t *TIDMapper) OnEdgeAdded(e *graph.Edge) {
	t.onEdgeEvent(e)
}

func NewTIDMapper(g *graph.Graph) *TIDMapper {
	return &TIDMapper{
		Graph: g,
	}
}
