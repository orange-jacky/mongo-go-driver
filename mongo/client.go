// Copyright (C) MongoDB, Inc. 2017-present.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at http://www.apache.org/licenses/LICENSE-2.0

package mongo

import (
	"context"
	"time"

	"github.com/mongodb/mongo-go-driver/core/command"
	"github.com/mongodb/mongo-go-driver/core/connstring"
	"github.com/mongodb/mongo-go-driver/core/description"
	"github.com/mongodb/mongo-go-driver/core/dispatch"
	"github.com/mongodb/mongo-go-driver/core/options"
	"github.com/mongodb/mongo-go-driver/core/readconcern"
	"github.com/mongodb/mongo-go-driver/core/readpref"
	"github.com/mongodb/mongo-go-driver/core/topology"
	"github.com/mongodb/mongo-go-driver/core/writeconcern"
)

const defaultLocalThreshold = 15 * time.Millisecond

// Client performs operations on a given topology.
type Client struct {
	topologyOptions []topology.Option
	topology        *topology.Topology
	connString      connstring.ConnString
	localThreshold  time.Duration
	readPreference  *readpref.ReadPref
	readConcern     *readconcern.ReadConcern
	writeConcern    *writeconcern.WriteConcern
}

// Connect creates a new Client and then initializes it using the Connect method.
func Connect(ctx context.Context, uri string, opts *ClientOptions) (*Client, error) {
	c, err := NewClientWithOptions(uri, opts)
	if err != nil {
		return nil, err
	}
	err = c.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// NewClient creates a new client to connect to a cluster specified by the uri.
func NewClient(uri string) (*Client, error) {
	cs, err := connstring.Parse(uri)
	if err != nil {
		return nil, err
	}

	return newClient(cs, nil)
}

// NewClientWithOptions creates a new client to connect to to a cluster specified by the connection
// string and the options manually passed in. If the same option is configured in both the
// connection string and the manual options, the manual option will be ignored.
func NewClientWithOptions(uri string, opts *ClientOptions) (*Client, error) {
	cs, err := connstring.Parse(uri)
	if err != nil {
		return nil, err
	}

	return newClient(cs, opts)
}

// NewClientFromConnString creates a new client to connect to a cluster, with configuration
// specified by the connection string.
func NewClientFromConnString(cs connstring.ConnString) (*Client, error) {
	return newClient(cs, nil)
}

// Connect initializes the Client by starting background monitoring goroutines.
// This method must be called before a Client can be used.
func (c *Client) Connect(ctx context.Context) error {
	return c.topology.Connect(ctx)
}

// Disconnect closes sockets to the topology referenced by this Client. It will
// shut down any monitoring goroutines, close the idle connection pool, and will
// wait until all the in use connections have been returned to the connection
// pool and closed before returning. If the context expires via cancellation,
// deadline, or timeout before the in use connections have returned, the in use
// connections will be closed, resulting in the failure of any in flight read
// or write operations. If this method returns with no errors, all connections
// associated with this Client have been closed.
func (c *Client) Disconnect(ctx context.Context) error {
	return c.topology.Disconnect(ctx)
}

func newClient(cs connstring.ConnString, opts *ClientOptions) (*Client, error) {
	client := &Client{
		connString:     cs,
		localThreshold: defaultLocalThreshold,
		readPreference: readpref.Primary(),
	}

	if opts != nil {
		for opts.opt != nil {
			err := opts.opt(client)
			if err != nil {
				return nil, err
			}
			opts = opts.next
		}
	}

	topts := append(
		client.topologyOptions,
		topology.WithConnString(func(connstring.ConnString) connstring.ConnString { return client.connString }),
	)
	topo, err := topology.New(topts...)
	if err != nil {
		return nil, err
	}

	client.topology = topo
	client.readConcern = readConcernFromConnString(&client.connString)
	client.writeConcern = writeConcernFromConnString(&client.connString)

	return client, nil
}

func readConcernFromConnString(cs *connstring.ConnString) *readconcern.ReadConcern {
	if len(cs.ReadConcernLevel) == 0 {
		return nil
	}

	rc := &readconcern.ReadConcern{}
	readconcern.Level(cs.ReadConcernLevel)(rc)

	return rc
}

func writeConcernFromConnString(cs *connstring.ConnString) *writeconcern.WriteConcern {
	var wc *writeconcern.WriteConcern

	if len(cs.WString) > 0 {
		if wc == nil {
			wc = writeconcern.New()
		}

		writeconcern.WTagSet(cs.WString)(wc)
	} else if cs.WNumberSet {
		if wc == nil {
			wc = writeconcern.New()
		}

		writeconcern.W(cs.WNumber)(wc)
	}

	if cs.JSet {
		if wc == nil {
			wc = writeconcern.New()
		}

		writeconcern.J(cs.J)(wc)
	}

	if cs.WTimeoutSet {
		if wc == nil {
			wc = writeconcern.New()
		}

		writeconcern.WTimeout(cs.WTimeout)(wc)
	}

	return wc
}

// Database returns a handle for a given database.
func (c *Client) Database(name string) *Database {
	return newDatabase(c, name)
}

// ConnectionString returns the connection string of the cluster the client is connected to.
func (c *Client) ConnectionString() string {
	return c.connString.Original
}

func (c *Client) listDatabasesHelper(ctx context.Context, filter interface{},
	nameOnly bool) (ListDatabasesResult, error) {

	f, err := TransformDocument(filter)
	if err != nil {
		return ListDatabasesResult{}, err
	}

	opts := []options.ListDatabasesOptioner{}

	if nameOnly {
		opts = append(opts, options.OptNameOnly(nameOnly))
	}

	cmd := command.ListDatabases{Filter: f, Opts: opts}

	// The spec indicates that we should not run the listDatabase command on a secondary in a
	// replica set.
	res, err := dispatch.ListDatabases(ctx, cmd, c.topology, description.ReadPrefSelector(readpref.Primary()))
	if err != nil {
		return ListDatabasesResult{}, err
	}
	return (ListDatabasesResult{}).fromResult(res), nil
}

// ListDatabases returns a ListDatabasesResult.
func (c *Client) ListDatabases(ctx context.Context, filter interface{}) (ListDatabasesResult, error) {
	return c.listDatabasesHelper(ctx, filter, false)
}

// ListDatabaseNames returns a slice containing the names of all of the databases on the server.
func (c *Client) ListDatabaseNames(ctx context.Context, filter interface{}) ([]string, error) {
	res, err := c.listDatabasesHelper(ctx, filter, true)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0)
	for _, spec := range res.Databases {
		names = append(names, spec.Name)
	}

	return names, nil
}
