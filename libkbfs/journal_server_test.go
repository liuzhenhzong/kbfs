// Copyright 2016 Keybase Inc. All rights reserved.
// Use of this source code is governed by a BSD
// license that can be found in the LICENSE file.

package libkbfs

import (
	"os"
	"testing"

	"github.com/keybase/kbfs/ioutil"
	"github.com/keybase/kbfs/kbfsblock"
	"github.com/keybase/kbfs/kbfscrypto"
	"github.com/keybase/kbfs/tlf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/context"
)

func setupJournalServerTest(t *testing.T) (
	tempdir string, ctx context.Context, cancel context.CancelFunc,
	config *ConfigLocal, jServer *JournalServer) {
	tempdir, err := ioutil.TempDir(os.TempDir(), "journal_server")
	require.NoError(t, err)

	// Clean up the tempdir if the rest of the setup fails.
	setupSucceeded := false
	defer func() {
		if !setupSucceeded {
			err := ioutil.RemoveAll(tempdir)
			assert.NoError(t, err)
		}
	}()

	ctx, cancel = context.WithTimeout(
		context.Background(), individualTestTimeout)

	// Clean up the context if the rest of the setup fails.
	defer func() {
		if !setupSucceeded {
			cancel()
		}
	}()

	config = MakeTestConfigOrBust(t, "test_user1", "test_user2")

	// Clean up the config if the rest of the setup fails.
	defer func() {
		if !setupSucceeded {
			ctx := context.Background()
			CheckConfigAndShutdown(ctx, t, config)
		}
	}()

	err = config.EnableJournaling(
		ctx, tempdir, TLFJournalBackgroundWorkEnabled)
	require.NoError(t, err)
	jServer, err = GetJournalServer(config)
	require.NoError(t, err)

	setupSucceeded = true
	return tempdir, ctx, cancel, config, jServer
}

func teardownJournalServerTest(
	t *testing.T, tempdir string, ctx context.Context,
	cancel context.CancelFunc, config Config) {
	CheckConfigAndShutdown(ctx, t, config)
	cancel()
	err := ioutil.RemoveAll(tempdir)
	assert.NoError(t, err)
}

func TestJournalServerRestart(t *testing.T) {
	tempdir, ctx, cancel, config, jServer := setupJournalServerTest(t)
	defer teardownJournalServerTest(t, tempdir, ctx, cancel, config)

	// Use a shutdown-only BlockServer so that it errors if the
	// journal tries to access it.
	jServer.delegateBlockServer = shutdownOnlyBlockServer{}

	tlfID := tlf.FakeID(2, false)
	err := jServer.Enable(ctx, tlfID, TLFJournalBackgroundWorkPaused)
	require.NoError(t, err)

	blockServer := config.BlockServer()
	mdOps := config.MDOps()

	h, err := ParseTlfHandle(ctx, config.KBPKI(), "test_user1", false)
	require.NoError(t, err)
	uid := h.ResolvedWriters()[0]

	// Put a block.

	bCtx := kbfsblock.MakeFirstContext(uid)
	data := []byte{1, 2, 3, 4}
	bID, err := kbfsblock.MakePermanentID(data)
	require.NoError(t, err)
	serverHalf, err := kbfscrypto.MakeRandomBlockCryptKeyServerHalf()
	require.NoError(t, err)
	err = blockServer.Put(ctx, tlfID, bID, bCtx, data, serverHalf)
	require.NoError(t, err)

	// Put an MD.

	rmd, err := makeInitialRootMetadata(config.MetadataVersion(), tlfID, h)
	require.NoError(t, err)
	rekeyDone, _, err := config.KeyManager().Rekey(ctx, rmd, false)
	require.NoError(t, err)
	require.True(t, rekeyDone)

	_, err = mdOps.Put(ctx, rmd)
	require.NoError(t, err)

	// Simulate a restart.

	jServer = makeJournalServer(
		config, jServer.log, tempdir, jServer.delegateBlockCache,
		jServer.delegateDirtyBlockCache,
		jServer.delegateBlockServer, jServer.delegateMDOps, nil, nil,
		jServer.diskLimiter)
	uid, verifyingKey, err :=
		getCurrentUIDAndVerifyingKey(ctx, config.KBPKI())
	require.NoError(t, err)
	err = jServer.EnableExistingJournals(
		ctx, uid, verifyingKey, TLFJournalBackgroundWorkPaused)
	require.NoError(t, err)
	config.SetBlockCache(jServer.blockCache())
	config.SetBlockServer(jServer.blockServer())
	config.SetMDOps(jServer.mdOps())

	// Get the block.

	buf, key, err := blockServer.Get(ctx, tlfID, bID, bCtx)
	require.NoError(t, err)
	require.Equal(t, data, buf)
	require.Equal(t, serverHalf, key)

	// Get the MD.

	head, err := mdOps.GetForTLF(ctx, tlfID)
	require.NoError(t, err)
	require.Equal(t, rmd.Revision(), head.Revision())
}

func TestJournalServerLogOutLogIn(t *testing.T) {
	tempdir, ctx, cancel, config, jServer := setupJournalServerTest(t)
	defer teardownJournalServerTest(t, tempdir, ctx, cancel, config)

	// Use a shutdown-only BlockServer so that it errors if the
	// journal tries to access it.
	jServer.delegateBlockServer = shutdownOnlyBlockServer{}

	tlfID := tlf.FakeID(2, false)
	err := jServer.Enable(ctx, tlfID, TLFJournalBackgroundWorkPaused)
	require.NoError(t, err)

	blockServer := config.BlockServer()
	mdOps := config.MDOps()

	h, err := ParseTlfHandle(ctx, config.KBPKI(), "test_user1", false)
	require.NoError(t, err)
	uid := h.ResolvedWriters()[0]

	// Put a block.

	bCtx := kbfsblock.MakeFirstContext(uid)
	data := []byte{1, 2, 3, 4}
	bID, err := kbfsblock.MakePermanentID(data)
	require.NoError(t, err)
	serverHalf, err := kbfscrypto.MakeRandomBlockCryptKeyServerHalf()
	require.NoError(t, err)
	err = blockServer.Put(ctx, tlfID, bID, bCtx, data, serverHalf)
	require.NoError(t, err)

	// Put an MD.

	rmd, err := makeInitialRootMetadata(config.MetadataVersion(), tlfID, h)
	require.NoError(t, err)
	rekeyDone, _, err := config.KeyManager().Rekey(ctx, rmd, false)
	require.NoError(t, err)
	require.True(t, rekeyDone)

	_, err = mdOps.Put(ctx, rmd)
	require.NoError(t, err)

	// Simulate a log out.

	serviceLoggedOut(ctx, config)

	// Get the block, which should fail.

	_, _, err = blockServer.Get(ctx, tlfID, bID, bCtx)
	require.IsType(t, kbfsblock.BServerErrorBlockNonExistent{}, err)

	// Get the head, which should be empty.

	head, err := mdOps.GetForTLF(ctx, tlfID)
	require.NoError(t, err)
	require.Equal(t, ImmutableRootMetadata{}, head)

	serviceLoggedIn(
		ctx, config, "test_user1", TLFJournalBackgroundWorkPaused)

	// Get the block.

	buf, key, err := blockServer.Get(ctx, tlfID, bID, bCtx)
	require.NoError(t, err)
	require.Equal(t, data, buf)
	require.Equal(t, serverHalf, key)

	// Get the MD.

	head, err = mdOps.GetForTLF(ctx, tlfID)
	require.NoError(t, err)
	require.Equal(t, rmd.Revision(), head.Revision())
}

func TestJournalServerLogOutDirtyOp(t *testing.T) {
	tempdir, ctx, cancel, config, jServer := setupJournalServerTest(t)
	defer teardownJournalServerTest(t, tempdir, ctx, cancel, config)

	tlfID := tlf.FakeID(2, false)
	err := jServer.Enable(ctx, tlfID, TLFJournalBackgroundWorkPaused)
	require.NoError(t, err)

	jServer.dirtyOpStart(tlfID)
	go func() {
		jServer.dirtyOpEnd(tlfID)
	}()

	// Should wait for the dirtyOpEnd call to happen and then
	// finish.
	//
	// TODO: Ideally, this test would be deterministic, i.e. we
	// detect when serviceLoggedOut blocks on waiting for
	// dirtyOpEnd, and only then do we call dirtyOpEnd.
	serviceLoggedOut(ctx, config)

	dirtyOps := func() uint {
		jServer.lock.RLock()
		defer jServer.lock.RUnlock()
		return jServer.dirtyOps
	}
	require.NotEqual(t, 0, dirtyOps)
}

func TestJournalServerMultiUser(t *testing.T) {
	tempdir, ctx, cancel, config, jServer := setupJournalServerTest(t)
	defer teardownJournalServerTest(t, tempdir, ctx, cancel, config)

	// Use a shutdown-only BlockServer so that it errors if the
	// journal tries to access it.
	jServer.delegateBlockServer = shutdownOnlyBlockServer{}

	tlfID := tlf.FakeID(2, false)
	err := jServer.Enable(ctx, tlfID, TLFJournalBackgroundWorkPaused)
	require.NoError(t, err)

	blockServer := config.BlockServer()
	mdOps := config.MDOps()

	h, err := ParseTlfHandle(
		ctx, config.KBPKI(), "test_user1,test_user2", false)
	require.NoError(t, err)
	uid1 := h.ResolvedWriters()[0]
	uid2 := h.ResolvedWriters()[1]

	// Put a block under user 1.

	bCtx1 := kbfsblock.MakeFirstContext(uid1)
	data1 := []byte{1, 2, 3, 4}
	bID1, err := kbfsblock.MakePermanentID(data1)
	require.NoError(t, err)
	serverHalf1, err := kbfscrypto.MakeRandomBlockCryptKeyServerHalf()
	require.NoError(t, err)
	err = blockServer.Put(ctx, tlfID, bID1, bCtx1, data1, serverHalf1)
	require.NoError(t, err)

	// Put an MD under user 1.

	rmd1, err := makeInitialRootMetadata(config.MetadataVersion(), tlfID, h)
	require.NoError(t, err)
	rmd1.SetLastModifyingWriter(uid1)
	rekeyDone, _, err := config.KeyManager().Rekey(ctx, rmd1, false)
	require.NoError(t, err)
	require.True(t, rekeyDone)

	_, err = mdOps.Put(ctx, rmd1)
	require.NoError(t, err)

	// Log in user 2.

	serviceLoggedOut(ctx, config)

	service := config.KeybaseService().(*KeybaseDaemonLocal)
	service.setCurrentUID(uid2)
	SwitchDeviceForLocalUserOrBust(t, config, 0)

	serviceLoggedIn(
		ctx, config, "test_user2", TLFJournalBackgroundWorkPaused)

	err = jServer.Enable(ctx, tlfID, TLFJournalBackgroundWorkPaused)
	require.NoError(t, err)

	// None of user 1's changes should be visible.

	_, _, err = blockServer.Get(ctx, tlfID, bID1, bCtx1)
	require.IsType(t, kbfsblock.BServerErrorBlockNonExistent{}, err)

	head, err := mdOps.GetForTLF(ctx, tlfID)
	require.NoError(t, err)
	require.Equal(t, ImmutableRootMetadata{}, head)

	// Put a block under user 2.

	bCtx2 := kbfsblock.MakeFirstContext(uid2)
	data2 := []byte{1, 2, 3, 4, 5}
	bID2, err := kbfsblock.MakePermanentID(data2)
	require.NoError(t, err)
	serverHalf2, err := kbfscrypto.MakeRandomBlockCryptKeyServerHalf()
	require.NoError(t, err)
	err = blockServer.Put(ctx, tlfID, bID2, bCtx2, data2, serverHalf2)
	require.NoError(t, err)

	// Put an MD under user 2.

	rmd2, err := makeInitialRootMetadata(config.MetadataVersion(), tlfID, h)
	require.NoError(t, err)
	rmd2.SetLastModifyingWriter(uid2)
	rekeyDone, _, err = config.KeyManager().Rekey(ctx, rmd2, false)
	require.NoError(t, err)
	require.True(t, rekeyDone)

	_, err = mdOps.Put(ctx, rmd2)
	require.NoError(t, err)

	// Log out.

	serviceLoggedOut(ctx, config)

	// No block or MD should be visible.

	_, _, err = blockServer.Get(ctx, tlfID, bID1, bCtx1)
	require.IsType(t, kbfsblock.BServerErrorBlockNonExistent{}, err)

	_, _, err = blockServer.Get(ctx, tlfID, bID2, bCtx2)
	require.IsType(t, kbfsblock.BServerErrorBlockNonExistent{}, err)

	head, err = mdOps.GetForTLF(ctx, tlfID)
	require.NoError(t, err)
	require.Equal(t, ImmutableRootMetadata{}, head)

	// Log in user 1.

	service.setCurrentUID(uid1)
	SwitchDeviceForLocalUserOrBust(t, config, 0)

	serviceLoggedIn(
		ctx, config, "test_user1", TLFJournalBackgroundWorkPaused)

	// Only user 1's block and MD should be visible.

	buf, key, err := blockServer.Get(ctx, tlfID, bID1, bCtx1)
	require.NoError(t, err)
	require.Equal(t, data1, buf)
	require.Equal(t, serverHalf1, key)

	_, _, err = blockServer.Get(ctx, tlfID, bID2, bCtx2)
	require.IsType(t, kbfsblock.BServerErrorBlockNonExistent{}, err)

	head, err = mdOps.GetForTLF(ctx, tlfID)
	require.NoError(t, err)
	require.Equal(t, uid1, head.LastModifyingWriter())

	// Log in user 2.

	serviceLoggedOut(ctx, config)

	service.setCurrentUID(uid2)
	SwitchDeviceForLocalUserOrBust(t, config, 0)

	serviceLoggedIn(
		ctx, config, "test_user2", TLFJournalBackgroundWorkPaused)

	// Only user 2's block and MD should be visible.

	_, _, err = blockServer.Get(ctx, tlfID, bID1, bCtx1)
	require.IsType(t, kbfsblock.BServerErrorBlockNonExistent{}, err)

	buf, key, err = blockServer.Get(ctx, tlfID, bID2, bCtx2)
	require.NoError(t, err)
	require.Equal(t, data2, buf)
	require.Equal(t, serverHalf2, key)

	head, err = mdOps.GetForTLF(ctx, tlfID)
	require.NoError(t, err)
	require.Equal(t, uid2, head.LastModifyingWriter())
}

func TestJournalServerEnableAuto(t *testing.T) {
	tempdir, ctx, cancel, config, jServer := setupJournalServerTest(t)
	defer teardownJournalServerTest(t, tempdir, ctx, cancel, config)

	tlfID := tlf.FakeID(2, false)
	err := jServer.EnableAuto(ctx)
	require.NoError(t, err)

	status, tlfIDs := jServer.Status(ctx)
	require.True(t, status.EnableAuto)
	require.Zero(t, status.JournalCount)
	require.Len(t, tlfIDs, 0)

	blockServer := config.BlockServer()
	h, err := ParseTlfHandle(ctx, config.KBPKI(), "test_user1", false)
	require.NoError(t, err)
	uid := h.ResolvedWriters()[0]

	// Access a TLF, which should create a journal automatically.
	bCtx := kbfsblock.MakeFirstContext(uid)
	data := []byte{1, 2, 3, 4}
	bID, err := kbfsblock.MakePermanentID(data)
	require.NoError(t, err)
	serverHalf, err := kbfscrypto.MakeRandomBlockCryptKeyServerHalf()
	require.NoError(t, err)
	err = blockServer.Put(ctx, tlfID, bID, bCtx, data, serverHalf)
	require.NoError(t, err)

	status, tlfIDs = jServer.Status(ctx)
	require.True(t, status.EnableAuto)
	require.Equal(t, 1, status.JournalCount)
	require.Len(t, tlfIDs, 1)

	// Flush the journal so it's not still being operated on by
	// another instance after the restart.
	err = jServer.Flush(ctx, tlfID)
	require.NoError(t, err)

	// Simulate a restart.
	jServer = makeJournalServer(
		config, jServer.log, tempdir, jServer.delegateBlockCache,
		jServer.delegateDirtyBlockCache,
		jServer.delegateBlockServer, jServer.delegateMDOps, nil, nil,
		jServer.diskLimiter)
	uid, verifyingKey, err :=
		getCurrentUIDAndVerifyingKey(ctx, config.KBPKI())
	require.NoError(t, err)
	err = jServer.EnableExistingJournals(
		ctx, uid, verifyingKey, TLFJournalBackgroundWorkPaused)
	require.NoError(t, err)
	status, tlfIDs = jServer.Status(ctx)
	require.True(t, status.EnableAuto)
	require.Equal(t, 1, status.JournalCount)
	require.Len(t, tlfIDs, 1)
}
