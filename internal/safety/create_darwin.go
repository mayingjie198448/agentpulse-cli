//go:build darwin

package safety

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

func createRunRoot(opts createOpts) (RunRecord, error) {
	if err := ensureExistingSentinel(opts.Sentinel); err != nil {
		return RunRecord{}, err
	}
	sentFD, sentSt, err := openFileNoFollow(opts.Sentinel)
	if err != nil {
		if os.IsNotExist(err) {
			return RunRecord{}, fmt.Errorf("%w: sentinel missing (ENOENT); not proof of denied access", ErrSentinelUnusable)
		}
		return RunRecord{}, fmt.Errorf("%w: %v", ErrSentinelUnusable, err)
	}
	sentPath, err := fdPath(sentFD)
	unix.Close(sentFD)
	if err != nil {
		return RunRecord{}, err
	}
	sdev, sino := ident(sentSt)

	base := opts.BaseDir
	if base == "" {
		base = "/private/tmp/agentpulse-runs"
	}
	baseFD, _, err := createSafeDarwinDir(base, protectedHomes(opts.RealHome))
	if err != nil {
		return RunRecord{}, err
	}
	defer unix.Close(baseFD)

	runID, err := randomHex(16)
	if err != nil {
		return RunRecord{}, err
	}
	nonce, err := randomHex(16)
	if err != nil {
		return RunRecord{}, err
	}
	if err := unix.Mkdirat(baseFD, runID, 0700); err != nil {
		return RunRecord{}, err
	}
	rootFD, err := unix.Openat(baseFD, runID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return RunRecord{}, err
	}
	defer unix.Close(rootFD)
	var st unix.Stat_t
	if err := unix.Fstat(rootFD, &st); err != nil {
		return RunRecord{}, err
	}
	dev, ino := ident(st)
	rootPath, err := fdPath(rootFD)
	if err != nil {
		return RunRecord{}, err
	}
	for _, home := range protectedHomes(opts.RealHome) {
		if withinPath(home, rootPath) {
			return RunRecord{}, fmt.Errorf("%w: run root is inside a protected home", ErrRefused)
		}
	}
	if withinPath(rootPath, sentPath) {
		return RunRecord{}, fmt.Errorf("%w: sentinel must already exist outside the run root", ErrSentinelUnusable)
	}
	if err := bindDarwinSyntheticProbe(runID, rootPath, nonce, sentPath, sdev, sino); err != nil {
		return RunRecord{}, err
	}

	for _, name := range []string{"home", "work", "tmp", "xdg"} {
		if err := unix.Mkdirat(rootFD, name, 0700); err != nil {
			return RunRecord{}, err
		}
	}
	xdgFD, err := unix.Openat(rootFD, "xdg", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return RunRecord{}, err
	}
	for _, name := range []string{"config", "data", "cache", "state"} {
		if err := unix.Mkdirat(xdgFD, name, 0700); err != nil {
			unix.Close(xdgFD)
			return RunRecord{}, err
		}
	}
	unix.Close(xdgFD)

	prof := darwinProfileIdentity(runID, rootPath, sentPath, dev, ino, sdev, sino)
	rec := RunRecord{
		RunID:         runID,
		Root:          rootPath,
		Work:          rootPath + "/work",
		Home:          rootPath + "/home",
		XDGConfigHome: rootPath + "/xdg/config",
		XDGDataHome:   rootPath + "/xdg/data",
		XDGCacheHome:  rootPath + "/xdg/cache",
		XDGStateHome:  rootPath + "/xdg/state",
		Tmp:           rootPath + "/tmp",
		Dev:           dev,
		Ino:           ino,
		Nonce:         nonce,
		ProfileHash:   profileHash(prof),
		ExeSHA256:     opts.ExeHash,
		Sentinel:      sentPath,
		SentinelDev:   sdev,
		SentinelIno:   sino,
		CreatedUnix:   time.Now().Unix(),
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return RunRecord{}, err
	}
	body = append(body, '\n')
	mfd, err := unix.Openat(rootFD, markerName, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return RunRecord{}, err
	}
	err = writeFD(mfd, body)
	unix.Close(mfd)
	if err != nil {
		return RunRecord{}, err
	}

	regDir := opts.RegistryDir
	if regDir == "" {
		regDir = "/private/tmp/agentpulse-run-registry"
	}
	regFD, regPath, err := createSafeDarwinDir(regDir, protectedHomes(opts.RealHome))
	if err != nil {
		return RunRecord{}, err
	}
	unix.Close(regFD)
	if withinPath(rootPath, regPath) || regPath == rootPath {
		return RunRecord{}, fmt.Errorf("%w: registry must stay outside the run root", ErrRefused)
	}
	rec.RegistryDir = regDir
	rec.RegistryPath = filepath.Join(regPath, runID+".json")
	rec.MarkerPath = rootPath + "/" + markerName
	if err := writeJSON(rec.RegistryPath, rec, 0600); err != nil {
		return RunRecord{}, err
	}
	return rec, nil
}

func createSyntheticSentinel(base string) (string, error) {
	dir := "/private/tmp/agentpulse-sentinels"
	if strings.TrimSpace(base) != "" {
		parent := filepath.Dir(filepath.Clean(base))
		if parent != "" && parent != "." && parent != "/" {
			dir = filepath.Join(parent, "agentpulse-sentinels")
		}
	}
	dirFD, kpath, err := createSafeDarwinDir(dir, protectedHomes(""))
	if err != nil {
		return "", err
	}
	defer unix.Close(dirFD)
	name, err := randomHex(8)
	if err != nil {
		return "", err
	}
	name = "sentinel-" + name
	fd, err := unix.Openat(dirFD, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	if err := writeFD(fd, []byte("sentinel-v1\n")); err != nil {
		return "", err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return "", fmt.Errorf("%w: stat synthetic sentinel: %v", ErrSentinelUnusable, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return "", fmt.Errorf("%w: synthetic sentinel is not a regular file", ErrSentinelUnusable)
	}
	dev, ino := ident(st)
	if dev == 0 || ino == 0 {
		return "", fmt.Errorf("%w: synthetic sentinel has zero device or inode", ErrSentinelUnusable)
	}
	sentinelPath, err := fdPath(fd)
	if err != nil {
		sentinelPath = kpath + "/" + name
	}
	if err := registerPendingDarwinSyntheticProbe(sentinelPath, dev, ino); err != nil {
		return "", err
	}
	return sentinelPath, nil
}

func createSafeDarwinDir(target string, homes []string) (int, string, error) {
	statFn := func(path string) (bool, bool, error) {
		fd, err := openDirNoFollow(path)
		if err != nil {
			if os.IsNotExist(err) {
				return false, false, nil
			}
			return false, false, err
		}
		defer unix.Close(fd)
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return false, true, err
		}
		kpath, err := fdPath(fd)
		if err != nil {
			return false, true, err
		}
		for _, home := range homes {
			if strings.TrimSpace(home) != "" && withinPath(home, kpath) {
				return false, true, fmt.Errorf("%w: ancestor %q is inside protected home %q", ErrRefused, kpath, home)
			}
		}
		return st.Mode&unix.S_IFMT == unix.S_IFDIR, true, nil
	}

	plan, err := planDarwinSafeDir(target, homes, statFn)
	if err != nil {
		return -1, "", err
	}

	currFD, err := openDirNoFollow(plan.ExistingAncestor)
	if err != nil {
		return -1, "", fmt.Errorf("%w: failed to open existing ancestor %s: %v", ErrRefused, plan.ExistingAncestor, err)
	}
	ancPath, err := fdPath(currFD)
	if err != nil {
		unix.Close(currFD)
		return -1, "", err
	}
	for _, home := range homes {
		if strings.TrimSpace(home) != "" && withinPath(home, ancPath) {
			unix.Close(currFD)
			return -1, "", fmt.Errorf("%w: ancestor descriptor in protected home", ErrRefused)
		}
	}

	for _, seg := range plan.MissingSegments {
		if err := unix.Mkdirat(currFD, seg, 0700); err != nil {
			unix.Close(currFD)
			return -1, "", fmt.Errorf("%w: handle-relative mkdirat failed for %s: %v", ErrRefused, seg, err)
		}
		nextFD, err := unix.Openat(currFD, seg, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(currFD)
		if err != nil {
			return -1, "", fmt.Errorf("%w: handle-relative openat failed for %s: %v", ErrRefused, seg, err)
		}
		currFD = nextFD
	}

	finalPath, err := fdPath(currFD)
	if err != nil {
		unix.Close(currFD)
		return -1, "", err
	}
	for _, home := range homes {
		if strings.TrimSpace(home) != "" && withinPath(home, finalPath) {
			unix.Close(currFD)
			return -1, "", fmt.Errorf("%w: final directory in protected home", ErrRefused)
		}
	}
	return currFD, finalPath, nil
}

func ensureExistingSentinel(path string) error {
	if err := darwinPathShapeError(path); err != nil {
		return fmt.Errorf("%w: %v", ErrSentinelUnusable, err)
	}
	fd, st, err := openFileNoFollow(path)
	if fd >= 0 {
		unix.Close(fd)
	}
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: sentinel missing (ENOENT); not proof of denied access", ErrSentinelUnusable)
		}
		return fmt.Errorf("%w: %v", ErrSentinelUnusable, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: sentinel is not an existing regular file", ErrSentinelUnusable)
	}
	return nil
}
