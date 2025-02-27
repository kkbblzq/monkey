package monkey

import (
	"fmt"
	"reflect"
	"sync"
	"unsafe"

	"github.com/huandu/go-tls/g"
)

var (
	lock = sync.Mutex{}

	patches = make(map[uintptr]*patch)
)

type PatchGuard struct {
	target      reflect.Value
	replacement reflect.Value
}

func (g *PatchGuard) Unpatch() {
	unpatchValue(g.target)
}

func (g *PatchGuard) Restore() {
	patchValue(g.target, g.replacement)
}

// Patch replaces a function with another
func Patch(target, replacement interface{}) *PatchGuard {
	t := reflect.ValueOf(target)
	r := reflect.ValueOf(replacement)
	patchValue(t, r)

	return &PatchGuard{t, r}
}

// PatchInstanceMethod replaces an instance method methodName for the type target with replacement
// Replacement should expect the receiver (of type target) as the first argument
func PatchInstanceMethod(target reflect.Type, methodName string, replacement interface{}) *PatchGuard {
	m, ok := target.MethodByName(methodName)
	if !ok {
		panic(fmt.Sprintf("unknown method %s", methodName))
	}
	r := reflect.ValueOf(replacement)
	patchValue(m.Func, r)

	return &PatchGuard{m.Func, r}
}

// See reflect.Value
type value struct {
	_   uintptr
	ptr unsafe.Pointer
}

func getPtr(v reflect.Value) unsafe.Pointer {
	return (*value)(unsafe.Pointer(&v)).ptr
}

func patchValue(target, replacement reflect.Value) {
	lock.Lock()
	defer lock.Unlock()

	if target.Kind() != reflect.Func {
		panic("target has to be a Func")
	}

	if replacement.Kind() != reflect.Func {
		panic("replacement has to be a Func")
	}

	if target.Type() != replacement.Type() {
		panic(fmt.Sprintf("target and replacement have to have the same type %s != %s", target.Type(), replacement.Type()))
	}

	p, ok := patches[target.Pointer()]
	if !ok {
		p = &patch{from: target.Pointer()}
		patches[target.Pointer()] = p
	}
	if !replacement.IsNil() {
		p.Add((uintptr)(getPtr(replacement)))
	}
	p.Apply()
}

// PatchEmpty patches target with empty patch.
// Call the target will run the original func.
func PatchEmpty(target interface{}) {
	lock.Lock()
	defer lock.Unlock()

	t := reflect.ValueOf(target).Pointer()

	p, ok := patches[t]
	if ok {
		return
	}

	p = &patch{from: t}
	patches[t] = p
	p.Apply()
}

// Unpatch removes any monkey patches on target
// returns whether target was patched in the first place
func Unpatch(target interface{}) bool {
	return unpatchValue(reflect.ValueOf(target))
}

// UnpatchInstanceMethod removes the patch on methodName of the target
// returns whether it was patched in the first place
func UnpatchInstanceMethod(target reflect.Type, methodName string) bool {
	m, ok := target.MethodByName(methodName)
	if !ok {
		panic(fmt.Sprintf("unknown method %s", methodName))
	}
	return unpatchValue(m.Func)
}

// UnpatchAll removes all applied monkeypatches
func UnpatchAll() {
	lock.Lock()
	defer lock.Unlock()
	for _, p := range patches {
		p.patches = nil
		p.Apply()
	}
}

// Unpatch removes a monkeypatch from the specified function
// returns whether the function was patched in the first place
func unpatchValue(target reflect.Value) bool {
	lock.Lock()
	defer lock.Unlock()
	patch, ok := patches[target.Pointer()]
	if !ok {
		return false
	}

	return patch.Del()
}

func unpatch(target uintptr, p *patch) {
	copyToLocation(target, p.original)
}

type patch struct {
	from uintptr

	original []byte
	patch    []byte

	patched bool

	// g pointer => patch func pointer
	patches map[uintptr]uintptr
}

func (p *patch) Add(to uintptr) {
	if p.patches == nil {
		p.patches = make(map[uintptr]uintptr)
	}

	gid := (uintptr)(g.G())

	if _, ok := p.patches[gid]; ok {
		panic("patch exists")
	}

	p.patches[gid] = to
}

func (p *patch) Del() bool {
	if p.patches == nil {
		return false
	}

	gid := (uintptr)(g.G())
	if _, ok := p.patches[gid]; !ok {
		return false
	}
	delete(p.patches, gid)
	p.Apply()
	return true
}

func (p *patch) Apply() {
	p.patch = p.Marshal()

	v := reflect.ValueOf(p.patch)
	allowExec(v.Pointer(), len(p.patch))

	if p.patched {
		data := littleEndian(v.Pointer())
		copyToLocation(p.from+2, data)
	} else {
		jumpData := jmpToFunctionValue(v.Pointer())
		copyToLocation(p.from, jumpData)
		p.patched = true
	}
}

func (p *patch) Marshal() (patch []byte) {
	if p.original == nil {
		p.original = alginPatch(p.from)
	}

	patch = getg()

	for g, to := range p.patches {
		t := jmpTable(g, to)
		patch = append(patch, t...)
	}

	patch = append(patch, p.original...)
	old := jmpToFunctionValue(p.from + uintptr(len(p.original)))
	patch = append(patch, old...)

	return
}
