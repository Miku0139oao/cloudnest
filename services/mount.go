package services

// Drive mounting. rclone exposes mount/mount, mount/unmount and
// mount/listmounts through the RC API; on Windows a mount is a drive letter
// backed by the WinFsp filesystem driver.
//
// MountCloud and UnmountCloud return dto.Empty, whose Err field carries the
// failure to the UI; every failure is also written to the application log so
// the reason is still available after the toast is gone.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cloudnest/internal/apperr"
	"cloudnest/internal/dto"
	"cloudnest/internal/platform"
	"cloudnest/internal/rc"
)

const (
	// mountTimeout bounds a mount/unmount request. Mounting can be slow on a
	// cold backend, so it gets a little more headroom than a plain RC call.
	mountTimeout = 30 * time.Second
	// mountListTimeout bounds mount/listmounts, which only reads local state.
	mountListTimeout = 10 * time.Second
	// mountVerifyTimeout bounds how long MountCloud waits for the new mount to
	// appear in mount/listmounts. mount/mount can return successfully before
	// WinFsp has attached the drive, so a successful reply alone is not proof.
	mountVerifyTimeout = 10 * time.Second
	// mountVerifyInterval is the pause between mount/listmounts polls.
	mountVerifyInterval = 250 * time.Millisecond
)

// MountCloud mounts one remote path as a Windows drive letter. driveLetter
// accepts "X", "X:" or "x". The remote path is relative to the account root.
func (s *Service) MountCloud(accountID, remotePath, driveLetter string) dto.Empty {
	letter, ok := normalizeDriveLetter(driveLetter)
	if !ok {
		s.mountWarn("mount: invalid drive letter", "letter", driveLetter)
		return dto.Empty{}
	}

	if !platform.WinFspInstalled() {
		return s.mountFail("mount: WinFsp is not installed", &dto.Fail{
			Code:    string(apperr.Unknown),
			Message: "WinFsp is required to mount drives",
			Detail:  "Install WinFsp from https://winfsp.dev, then restart Cloudnest and try again.",
		})
	}

	c, fail := s.client()
	if fail != nil {
		return s.mountFail("mount: engine not ready", fail)
	}

	fs, err := accountFS(accountID)
	if err != nil {
		return s.mountFail("mount: invalid account", failFrom(err))
	}
	remote, err := cleanRemotePathAllowEmpty(remotePath)
	if err != nil {
		return s.mountFail("mount: invalid remote path", failFrom(err))
	}

	if driveInUse(letter) {
		return s.mountFail("mount: drive letter is already in use", &dto.Fail{
			Code:    string(apperr.Unknown),
			Message: "drive letter " + letter + " is already in use",
			Detail:  "Choose another drive letter. FreeDriveLetters lists the ones which are available.",
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), mountTimeout)
	defer cancel()
	if err := c.Mount(ctx, fs+remote, letter); err != nil {
		return s.mountFail("mount: rclone mount failed", failFrom(err))
	}
	// rclone can accept mount/mount and still fail to attach the drive letter
	// (for example when WinFsp is installed but its service is not running).
	// Only report success once the mount is actually listed.
	if err := s.verifyMount(c, letter); err != nil {
		return s.mountFail("mount: mount did not become active", &dto.Fail{
			Code:    string(apperr.Unknown),
			Message: "drive " + letter + " was not mounted",
			Detail: "rclone accepted the mount request, but " + letter +
				" did not become available: " + err.Error() +
				". Check that WinFsp is installed and running and that the drive letter is free.",
			Retry: true,
		})
	}
	if s != nil && s.log != nil {
		s.log.Info("mount: mounted", "account", accountID, "path", remote, "mountPoint", letter)
	}
	return dto.Empty{}
}

// verifyMount polls mount/listmounts until mountPoint is listed or the bounded
// verification timeout expires. The mount daemon is the only authoritative
// source: the mount/mount reply does not guarantee that the drive is usable.
func (s *Service) verifyMount(c *rc.Client, mountPoint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), mountVerifyTimeout)
	defer cancel()
	return waitForMount(ctx, mountPoint, mountVerifyTimeout, mountVerifyInterval,
		func(ctx context.Context) (bool, error) {
			mounts, err := c.ListMounts(ctx)
			if err != nil {
				return false, err
			}
			for _, m := range mounts {
				if sameMountPoint(m.MountPoint, mountPoint) {
					return true, nil
				}
			}
			return false, nil
		})
}

// waitForMount calls visible until it reports the mount point or the timeout
// expires, whichever comes first. The last listing error, if any, is kept so
// the returned error explains why the mount could not be confirmed.
func waitForMount(ctx context.Context, mountPoint string, timeout, interval time.Duration,
	visible func(context.Context) (bool, error)) error {
	deadline := time.Now().Add(timeout)
	timeoutErr := func(lastErr error) error {
		if lastErr != nil {
			return fmt.Errorf("%s did not appear within %s: %w", mountPoint, timeout, lastErr)
		}
		return fmt.Errorf("%s did not appear within %s", mountPoint, timeout)
	}
	var lastErr error
	for {
		ok, err := visible(ctx)
		if ok {
			return nil
		}
		if err != nil {
			lastErr = err
		}
		if !time.Now().Before(deadline) {
			return timeoutErr(lastErr)
		}
		select {
		case <-ctx.Done():
			if lastErr == nil {
				lastErr = ctx.Err()
			}
			return timeoutErr(lastErr)
		case <-time.After(interval):
		}
	}
}

// sameMountPoint compares a listed mount point with the requested one. Drive
// letters are compared in their canonical "X:" form because rclone may echo
// back "X", "x:" or "X:\"; other mount points are compared as cleaned paths.
func sameMountPoint(listed, want string) bool {
	trim := func(s string) string {
		return strings.TrimRight(strings.TrimSpace(s), `\/`)
	}
	la, lok := normalizeDriveLetter(trim(listed))
	wa, wok := normalizeDriveLetter(trim(want))
	if lok && wok {
		return la == wa
	}
	return strings.EqualFold(filepath.Clean(listed), filepath.Clean(want))
}

// UnmountCloud removes one active mount. A bare drive letter is normalised the
// same way as MountCloud.
func (s *Service) UnmountCloud(mountPoint string) dto.Empty {
	mp := strings.TrimSpace(mountPoint)
	if mp == "" {
		s.mountWarn("mount: empty mount point")
		return dto.Empty{}
	}
	if letter, ok := normalizeDriveLetter(mp); ok {
		mp = letter
	}

	c, fail := s.client()
	if fail != nil {
		return s.mountFail("unmount: engine not ready", fail)
	}
	ctx, cancel := context.WithTimeout(context.Background(), mountTimeout)
	defer cancel()
	if err := c.Unmount(ctx, mp); err != nil {
		return s.mountFail("unmount: rclone unmount failed", failFrom(err))
	}
	if s != nil && s.log != nil {
		s.log.Info("mount: unmounted", "mountPoint", mp)
	}
	return dto.Empty{}
}

// ListMounts returns the active mounts together with the WinFsp availability
// flag the UI needs to decide whether mounting is possible.
func (s *Service) ListMounts() dto.MountList {
	out := dto.MountList{WinFsp: platform.WinFspInstalled()}

	c, fail := s.client()
	if fail != nil {
		out.Err = fail
		return out
	}
	ctx, cancel := context.WithTimeout(context.Background(), mountListTimeout)
	defer cancel()
	mounts, err := c.ListMounts(ctx)
	if err != nil {
		out.Err = failFrom(err)
		return out
	}
	out.Items = make([]dto.MountInfo, 0, len(mounts))
	for _, m := range mounts {
		out.Items = append(out.Items, dto.MountInfo{
			MountPoint: m.MountPoint,
			Fs:         m.Fs,
			MountedOn:  m.MountedOn,
		})
	}
	return out
}

// FreeDriveLetters lists the drive letters from D: to Z: which are not in use.
// A: and B: are skipped because Windows reserves them for floppy drives.
func (s *Service) FreeDriveLetters() []string {
	letters := make([]string, 0, 23)
	for ch := 'D'; ch <= 'Z'; ch++ {
		letter := string(ch) + ":"
		if !driveInUse(letter) {
			letters = append(letters, letter)
		}
	}
	return letters
}

// ---------------------------------------------------------------- helpers

// normalizeDriveLetter accepts "X", "X:" or "x" (surrounding space is ignored)
// and returns the canonical "X:" form.
func normalizeDriveLetter(raw string) (string, bool) {
	letter := strings.ToUpper(strings.TrimSpace(raw))
	letter = strings.TrimSuffix(letter, ":")
	if len(letter) != 1 || letter[0] < 'A' || letter[0] > 'Z' {
		return "", false
	}
	return letter + ":", true
}

// driveInUse reports whether a drive letter currently resolves. On Windows a
// free letter makes os.Stat fail; a used one succeeds.
func driveInUse(letter string) bool {
	_, err := os.Stat(letter + `\`)
	return err == nil
}

func (s *Service) mountWarn(msg string, args ...any) {
	if s != nil && s.log != nil {
		s.log.Warn(msg, args...)
	}
}

func (s *Service) mountFail(msg string, fail *dto.Fail) dto.Empty {
	if fail == nil {
		s.mountWarn(msg)
		return dto.Empty{}
	}
	s.mountWarn(msg, "code", fail.Code, "message", fail.Message, "detail", fail.Detail)
	return dto.Empty{Err: fail}
}
