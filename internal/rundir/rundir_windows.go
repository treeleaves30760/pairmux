//go:build windows

package rundir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	modadvapi32 = syscall.NewLazyDLL("advapi32.dll")
	modkernel32 = syscall.NewLazyDLL("kernel32.dll")

	procConvertStringSecurityDescriptorToSecurityDescriptorW = modadvapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")
	procGetNamedSecurityInfoW                                = modadvapi32.NewProc("GetNamedSecurityInfoW")
	procLocalFree                                            = modkernel32.NewProc("LocalFree")
)

const (
	sddlRevision1              = 1
	seFileObject               = 1
	ownerSecurityInformation   = 0x00000001
	tokenOwnerInformationClass = 4 // TokenOwner
)

func envRoot() string { return os.Getenv("PAIRMUX_TMPDIR") }

// dirUnder is under the per-user local application data folder by default.
func dirUnder(root string) (string, error) {
	if root == "" {
		cache, err := os.UserCacheDir() // %LocalAppData%
		if err != nil {
			return "", fmt.Errorf("locate %%LocalAppData%%: %w (set PAIRMUX_TMPDIR)", err)
		}
		root = filepath.Join(cache, "pairmux")
	}
	return filepath.Join(canonical(root), "run"), nil
}

// Ensure creates the runtime directory if needed and refuses one another user
// owns. It is created with a protected DACL admitting only the current user
// and SYSTEM, so a PAIRMUX_TMPDIR on a shared drive (whose inherited ACL may
// admit every local user) still yields a private socket: anyone who can
// connect to it can type into every terminal.
func Ensure() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(d), 0o700); err != nil {
		return "", fmt.Errorf("create runtime directory %s: %w (set PAIRMUX_TMPDIR to a writable directory)", d, err)
	}
	if err := createPrivateDir(d); err != nil && !errors.Is(err, os.ErrExist) {
		return "", fmt.Errorf("create runtime directory %s: %w (set PAIRMUX_TMPDIR to a writable directory)", d, err)
	}
	return d, check(d)
}

// Verify returns the runtime directory without creating it, after the same
// checks Ensure makes; a missing directory yields an error wrapping
// os.ErrNotExist. Clients call it before every connection.
func Verify() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return d, check(d)
}

func check(d string) error {
	fi, err := os.Lstat(d)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("runtime directory %s: %w", d, os.ErrNotExist)
		}
		return fmt.Errorf("runtime directory %s: %w", d, err)
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("runtime directory %s is not a plain directory", d)
	}
	owner, err := ownerSID(d)
	if err != nil {
		return fmt.Errorf("runtime directory %s: read owner: %w", d, err)
	}
	mine, err := currentOwnerSIDs()
	if err != nil {
		return fmt.Errorf("runtime directory %s: identify current user: %w", d, err)
	}
	for _, sid := range mine {
		if sid == owner {
			return nil
		}
	}
	return fmt.Errorf("runtime directory %s is owned by %s, not the current user", d, owner)
}

func createPrivateDir(d string) error {
	user, err := userSID()
	if err != nil {
		return err
	}
	sddl, err := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;" + user + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	var sd uintptr
	if r, _, err := procConvertStringSecurityDescriptorToSecurityDescriptorW.Call(
		uintptr(unsafe.Pointer(sddl)), sddlRevision1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return fmt.Errorf("build security descriptor: %w", err)
	}
	defer procLocalFree.Call(sd)
	path, err := syscall.UTF16PtrFromString(d)
	if err != nil {
		return err
	}
	sa := syscall.SecurityAttributes{Length: uint32(unsafe.Sizeof(syscall.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := syscall.CreateDirectory(path, &sa); err != nil {
		if errors.Is(err, syscall.ERROR_ALREADY_EXISTS) {
			return os.ErrExist
		}
		return err
	}
	return nil
}

func ownerSID(path string) (string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	var owner *syscall.SID
	var sd uintptr
	if rc, _, _ := procGetNamedSecurityInfoW.Call(uintptr(unsafe.Pointer(p)), seFileObject, ownerSecurityInformation,
		uintptr(unsafe.Pointer(&owner)), 0, 0, 0, uintptr(unsafe.Pointer(&sd))); rc != 0 {
		return "", syscall.Errno(rc)
	}
	defer procLocalFree.Call(sd)
	return owner.String()
}

func userSID() (string, error) {
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", err
	}
	defer t.Close()
	u, err := t.GetTokenUser()
	if err != nil {
		return "", err
	}
	return u.User.Sid.String()
}

// currentOwnerSIDs is the user and the token's default owner for new objects:
// an elevated administrator's files are owned by BUILTIN\Administrators.
func currentOwnerSIDs() ([]string, error) {
	user, err := userSID()
	if err != nil {
		return nil, err
	}
	sids := []string{user}
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return sids, nil
	}
	defer t.Close()
	var n uint32
	_ = syscall.GetTokenInformation(t, tokenOwnerInformationClass, nil, 0, &n)
	if n == 0 {
		return sids, nil
	}
	buf := make([]byte, n)
	if err := syscall.GetTokenInformation(t, tokenOwnerInformationClass, &buf[0], n, &n); err != nil {
		return sids, nil
	}
	// TOKEN_OWNER is a single PSID.
	owner := *(**syscall.SID)(unsafe.Pointer(&buf[0]))
	if s, err := owner.String(); err == nil && s != user {
		sids = append(sids, s)
	}
	return sids, nil
}
