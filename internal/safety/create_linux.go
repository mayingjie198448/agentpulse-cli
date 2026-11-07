//go:build linux

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

type createOpts struct {
	BaseDir     string
	RegistryDir string
	RealHome    string
	Sentinel    string
	ExeHash     string
}

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
		base = filepath.Join(os.TempDir(), "agentpulse-runs")
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return RunRecord{}, err
	}
	baseFD, err := openDirNoFollow(base)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: run base: %v", ErrRefused, err)
	}
	defer unix.Close(baseFD)
	basePath, err := fdPath(baseFD)
	if err != nil {
		return RunRecord{}, err
	}
	for _, home := range protectedHomes(opts.RealHome) {
		if withinPath(home, basePath) {
			return RunRecord{}, fmt.Errorf("%w: run base is inside a protected home", ErrRefused)
		}
	}

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

	abi, access, scoped, err := landlockMask()
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: %v", ErrBackendUnavailable, err)
	}
	prof := profileIdentity{
		RunID: runID, ABI: abi, AccessFS: access, Scoped: scoped,
		Root: rootPath, Dev: dev, Ino: ino,
		Sentinel: sentPath, SentinelDev: sdev, SentinelIno: sino,
	}
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
		regDir = DefaultRegistryDir()
	}
	if err := os.MkdirAll(regDir, 0700); err != nil {
		return RunRecord{}, err
	}
	regFD, err := openDirNoFollow(regDir)
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: registry: %v", ErrRefused, err)
	}
	regPath, err := fdPath(regFD)
	unix.Close(regFD)
	if err != nil {
		return RunRecord{}, err
	}
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
	dir := filepath.Join(os.TempDir(), "agentpulse-sentinels")
	if strings.TrimSpace(base) != "" {
		parent := filepath.Dir(filepath.Clean(base))
		if parent != "" && parent != "." && parent != string(filepath.Separator) {
			dir = filepath.Join(parent, "agentpulse-sentinels")
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	dirFD, err := openDirNoFollow(dir)
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
	err = writeFD(fd, []byte("sentinel-v1\n"))
	unix.Close(fd)
	if err != nil {
		return "", err
	}
	kpath, err := fdPath(dirFD)
	if err != nil {
		return "", err
	}
	return kpath + "/" + name, nil
}

func ensureExistingSentinel(path string) error {
	if err := pathShapeError(path); err != nil {
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
