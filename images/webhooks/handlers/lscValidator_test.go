/*
Copyright 2026 Flant JSC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package handlers

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	slv "github.com/deckhouse/sds-local-volume/api/v1alpha1"
)

// withNodeExistenceChecker swaps the package-level checker for the duration
// of a test and returns a restore function. Test-only helper.
func withNodeExistenceChecker(fn func(ctx context.Context, wanted []string) ([]string, error)) func() {
	prev := nodeExistenceChecker
	nodeExistenceChecker = fn
	return func() { nodeExistenceChecker = prev }
}

func makeRawFileLSC(nodes ...string) *slv.LocalStorageClass {
	rf := &slv.LocalStorageClassRawFileSpec{}
	for _, n := range nodes {
		rf.Nodes = append(rf.Nodes, slv.LocalStorageClassRawFileNode{Name: n})
	}
	return &slv.LocalStorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: "test-lsc"},
		Spec: slv.LocalStorageClassSpec{
			RawFile: rf,
		},
	}
}

func TestValidateRawFile_EmptyNodesAccepted(t *testing.T) {
	defer withNodeExistenceChecker(func(_ context.Context, _ []string) ([]string, error) {
		t.Fatal("checker must not be called when nodes list is empty")
		return nil, nil
	})()

	res, err := validateRawFile(context.Background(), makeRawFileLSC())
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

func TestValidateRawFile_RejectsEmptyNodeName(t *testing.T) {
	defer withNodeExistenceChecker(func(_ context.Context, _ []string) ([]string, error) {
		t.Fatal("checker must not be called when local validation fails")
		return nil, nil
	})()

	res, err := validateRawFile(context.Background(), makeRawFileLSC(""))
	require.NoError(t, err)
	assert.False(t, res.Valid)
	assert.Contains(t, res.Message, "must not be empty")
}

func TestValidateRawFile_RejectsDuplicateNode(t *testing.T) {
	defer withNodeExistenceChecker(func(_ context.Context, _ []string) ([]string, error) {
		t.Fatal("checker must not be called when local validation fails")
		return nil, nil
	})()

	res, err := validateRawFile(context.Background(), makeRawFileLSC("worker-1", "worker-1"))
	require.NoError(t, err)
	assert.False(t, res.Valid)
	assert.Contains(t, res.Message, "Duplicate node name")
}

func TestValidateRawFile_AcceptsExistingNodes(t *testing.T) {
	defer withNodeExistenceChecker(func(_ context.Context, wanted []string) ([]string, error) {
		assert.ElementsMatch(t, []string{"worker-1", "worker-2"}, wanted)
		return nil, nil
	})()

	res, err := validateRawFile(context.Background(), makeRawFileLSC("worker-1", "worker-2"))
	require.NoError(t, err)
	assert.True(t, res.Valid)
}

func TestValidateRawFile_RejectsMissingNodes(t *testing.T) {
	defer withNodeExistenceChecker(func(_ context.Context, _ []string) ([]string, error) {
		return []string{"worker-3"}, nil
	})()

	res, err := validateRawFile(context.Background(), makeRawFileLSC("worker-1", "worker-3"))
	require.NoError(t, err)
	assert.False(t, res.Valid)
	assert.Contains(t, res.Message, "worker-3")
}

func TestValidateRawFile_FailsOpenOnAPIError(t *testing.T) {
	defer withNodeExistenceChecker(func(_ context.Context, _ []string) ([]string, error) {
		return nil, errors.New("api unreachable")
	})()

	res, err := validateRawFile(context.Background(), makeRawFileLSC("worker-1"))
	require.NoError(t, err)
	assert.True(t, res.Valid, "transient API error must not block LSC edits")
}
