// Copyright 2019 The go-python Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// this version uses pybindgen and a generated .go file to do the binding

const (
	// GoHandle is the type to use for the Handle map key, go-side
	GoHandle = "int64"
	// CGoHandle is Handle for cgo files
	CGoHandle = "C.longlong"
	// PyHandle is within python
	PyHandle = "int64_t"
)

type BuildMode string

const (
	ModeGen   BuildMode = "gen"
	ModeBuild           = "build"
	ModeExe             = "exe"
	ModePkg             = "pkg"
)

// for all preambles: 1 = name of package (outname), 2 = cmdstr

// 3 = libcfg, 4 = GoHandle, 5 = CGoHandle, 6 = all imports, 7 = mainstr, 8 = exe pre C, 9 = exe pre go
const (
	goPreamble = `/*
cgo stubs for package %[1]s.
File is generated by gopy. Do not edit.
%[2]s
*/

package main

/*
#cgo pkg-config: %[3]s
// #define Py_LIMITED_API // need full API for PyRun*
#include <Python.h>
typedef uint8_t bool;
// static inline is trick for avoiding need for extra .c file
// the following are used for build value -- switch on reflect.Kind
// or the types equivalent
static inline PyObject* gopy_build_bool(uint8_t val) {
	return Py_BuildValue("b", val);
}
static inline PyObject* gopy_build_int64(int64_t val) {
	return Py_BuildValue("k", val);
}
static inline PyObject* gopy_build_uint64(uint64_t val) {
	return Py_BuildValue("K", val);
}
static inline PyObject* gopy_build_float64(double val) {
	return Py_BuildValue("d", val);
}
static inline PyObject* gopy_build_string(const char* val) {
	return Py_BuildValue("s", val);
}
static inline void gopy_decref(PyObject* obj) { // macro
	Py_XDECREF(obj);
}
static inline void gopy_incref(PyObject* obj) { // macro
	Py_XINCREF(obj);
}
static inline int gopy_method_check(PyObject* obj) { // macro
	return PyMethod_Check(obj);
}
static inline void gopy_err_handle() {
	if(PyErr_Occurred() != NULL) {
		PyErr_Print();
	}
}
%[8]s
*/
import "C"
import (
	"github.com/goki/gopy/gopyh" // handler
	%[6]s
)

// main doesn't do anything in lib / pkg mode, but is essential for exe mode
func main() {
	%[7]s
}

// initialization functions -- can be called from python after library is loaded
// GoPyInitRunFile runs a separate python file -- call in GoPyInit if it
// steals the main thread e.g., for GUI event loop, as in GoGi startup.

//export GoPyInit
func GoPyInit() {
	%[7]s
}

// type for the handle -- int64 for speed (can switch to string)
type GoHandle %[4]s
type CGoHandle %[5]s

// boolGoToPy converts a Go bool to python-compatible C.char
func boolGoToPy(b bool) C.char {
	if b {
		return 1
	}
	return 0
}

// boolPyToGo converts a python-compatible C.Char to Go bool
func boolPyToGo(b C.char) bool {
	if b != 0 {
		return true
	}
	return false
}

func complex64GoToPy(c complex64) *C.PyObject {
	return C.PyComplex_FromDoubles(C.double(real(c)), C.double(imag(c)))
}

func complex64PyToGo(o *C.PyObject) complex64 {
	v := C.PyComplex_AsCComplex(o)
	return complex(float32(v.real), float32(v.imag))
}

func complex128GoToPy(c complex128) *C.PyObject {
	return C.PyComplex_FromDoubles(C.double(real(c)), C.double(imag(c)))
}

func complex128PyToGo(o *C.PyObject) complex128 {
	v := C.PyComplex_AsCComplex(o)
	return complex(float64(v.real), float64(v.imag))
}

%[9]s
`

	goExePreambleC = `
#if PY_VERSION_HEX >= 0x03000000
extern PyObject* PyInit__%[1]s(void);
static inline void gopy_load_mod() {
	PyImport_AppendInittab("_%[1]s", PyInit__%[1]s);
}
#else
extern void* init__%[1]s(void);
static inline void gopy_load_mod() {
	PyImport_AppendInittab("_%[1]s", init__%[1]s);
}
#endif

`

	goExePreambleGo = `
// wchar version of startup args
var wargs []*C.wchar_t

//export GoPyMainRun
func GoPyMainRun() {
	// need to encode char* into wchar_t*
	for i := range os.Args {
		cstr := C.CString(os.Args[i])
		wargs = append(wargs, C.Py_DecodeLocale(cstr, nil))
		C.free(unsafe.Pointer(cstr))
	}
	C.gopy_load_mod()
	C.Py_Initialize()
	C.PyEval_InitThreads()
	C.Py_Main(C.int(len(wargs)), &wargs[0])
}

`

	PyBuildPreamble = `# python build stubs for package %[1]s
# File is generated by gopy. Do not edit.
# %[2]s

from pybindgen import retval, param, Module
import sys

mod = Module('_%[1]s')
mod.add_include('"%[1]s_go.h"')
mod.add_function('GoPyInit', None, [])
`

	// 3 = specific package name, 4 = spec pkg path, 5 = doc, 6 = imports
	PyWrapPreamble = `%[5]s
# python wrapper for package %[4]s within overall package %[1]s
# This is what you import to use the package.
# File is generated by gopy. Do not edit.
# %[2]s

# the following is required to enable dlopen to open the _go.so file
import os,sys,inspect,collections
cwd = os.getcwd()
currentdir = os.path.dirname(os.path.abspath(inspect.getfile(inspect.currentframe())))
os.chdir(currentdir)
import _%[1]s
os.chdir(cwd)

# to use this code in your end-user python file, import it as follows:
# from %[1]s import %[3]s
# and then refer to everything using %[3]s. prefix
# packages imported by this package listed below:

%[6]s

`

	// exe version of preamble -- doesn't need complex code to load _ module
	// 3 = specific package name, 4 = spec pkg path, 5 = doc, 6 = imports
	PyWrapExePreamble = `%[5]s
# python wrapper for package %[4]s within standalone executable package %[1]s
# This is what you import to use the package.
# File is generated by gopy. Do not edit.
# %[2]s

import _%[1]s, collections

# to use this code in your end-user python file, import it as follows:
# from %[1]s import %[3]s
# and then refer to everything using %[3]s. prefix
# packages imported by this package listed below:

%[6]s

`

	GoPkgDefs = `
import collections
	
class GoClass(object):
	"""GoClass is the base class for all GoPy wrapper classes"""
	def __init__(self):
		self.handle = 0

# use go.nil for nil pointers 
nil = GoClass()

# need to explicitly initialize it
def main():
	global nil
	nil = GoClass()

main()

def Init():
	"""calls the GoPyInit function, which runs the 'main' code string that was passed using -main arg to gopy"""
	_%[1]s.GoPyInit()

	`

	// 3 = gencmd, 4 = vm, 5 = libext 6 = extraGccArgs
	MakefileTemplate = `# Makefile for python interface for package %[1]s.
# File is generated by gopy. Do not edit.
# %[2]s

GOCMD=go
GOBUILD=$(GOCMD) build
GOIMPORTS=goimports
PYTHON=%[4]s
PYTHON_CFG=$(PYTHON)-config
LIBEXT=%[5]s

# get the CC and flags used to build python:
GCC = $(shell $(GOCMD) env CC)
CFLAGS = $(shell $(PYTHON_CFG) --cflags)
LDFLAGS = $(shell $(PYTHON_CFG) --ldflags)

all: gen build

gen:
	%[3]s

build:
	# build target builds the generated files -- this is what gopy build does..
	# this will otherwise be built during go build and may be out of date
	- rm %[1]s.c
	# goimports is needed to ensure that the imports list is valid
	$(GOIMPORTS) -w %[1]s.go
	# generate %[1]s_go$(LIBEXT) from %[1]s.go -- the cgo wrappers to go functions
	$(GOBUILD) -buildmode=c-shared -o %[1]s_go$(LIBEXT) %[1]s.go
	# use pybindgen to build the %[1]s.c file which are the CPython wrappers to cgo wrappers..
	# note: pip install pybindgen to get pybindgen if this fails
	$(PYTHON) build.py
	# build the _%[1]s$(LIBEXT) library that contains the cgo and CPython wrappers
	# generated %[1]s.py python wrapper imports this c-code package
	$(GCC) %[1]s.c %[6]s %[1]s_go$(LIBEXT) -o _%[1]s$(LIBEXT) $(CFLAGS) $(LDFLAGS) -w
	
`

	// exe version of template: 3 = gencmd, 4 = vm, 5 = libext
	MakefileExeTemplate = `# Makefile for python interface for standalone executable package %[1]s.
# File is generated by gopy. Do not edit.
# %[2]s

GOCMD=go
GOBUILD=$(GOCMD) build
GOIMPORTS=goimports
PYTHON=%[4]s
PYTHON_CFG=$(PYTHON)-config
LIBEXT=%[5]s

# get the flags used to build python:
GCC = $(shell $(GOCMD) env CC)
CFLAGS = $(shell $(PYTHON_CFG) --cflags)
LDFLAGS = $(shell $(PYTHON_CFG) --ldflags)

all: gen build

gen:
	%[3]s

build:
	# build target builds the generated files into exe -- this is what gopy build does..
	# goimports is needed to ensure that the imports list is valid
	$(GOIMPORTS) -w %[1]s.go
	# this will otherwise be built during go build and may be out of date
	- rm %[1]s.c 
	echo "typedef uint8_t bool;" > %[1]s_go.h
	# this will fail but is needed to generate the .c file that then allows go build to work
	- $(PYTHON) build.py >/dev/null 2>&1
	# generate %[1]s_go.h from %[1]s.go -- unfortunately no way to build .h only
	$(GOBUILD) -buildmode=c-shared -o %[1]s_go$(LIBEXT) >/dev/null 2>&1
	# use pybindgen to build the %[1]s.c file which are the CPython wrappers to cgo wrappers..
	# note: pip install pybindgen to get pybindgen if this fails
	$(PYTHON) build.py
	# build the executable
	- rm %[1]s_go$(LIBEXT)
	$(GOBUILD) -o py%[1]s
	
`
)

// thePyGen is the current pyGen which is needed in symbols to lookup
// package paths -- not very clean to pass around or add to various
// data structures to make local, but if that ends up being critical
// for some reason, it could be done.
var thePyGen *pyGen

// GenPyBind generates a .go file, build.py file to enable pybindgen to create python bindings,
// and wrapper .py file(s) that are loaded as the interface to the package with shadow
// python-side classes
// mode = gen, build, pkg, exe
func GenPyBind(mode BuildMode, odir, outname, cmdstr, vm, mainstr, libext, extragccargs string, lang int) error {
	gen := &pyGen{
		mode:         mode,
		odir:         odir,
		outname:      outname,
		cmdstr:       cmdstr,
		vm:           vm,
		mainstr:      mainstr,
		libext:       libext,
		extraGccArgs: extragccargs,
		lang:         lang,
	}
	gen.genPackageMap()
	thePyGen = gen
	err := gen.gen()
	thePyGen = nil
	if err != nil {
		return err
	}
	return err
}

type pyGen struct {
	gofile   *printer
	pybuild  *printer
	pywrap   *printer
	makefile *printer

	pkg    *Package // current package (only set when doing package-specific processing)
	err    ErrorList
	pkgmap map[string]struct{} // map of package paths

	mode         BuildMode // mode: gen, build, pkg, exe
	odir         string    // output directory
	outname      string    // overall output (package) name
	cmdstr       string    // overall command (embedded in generated files)
	vm           string    // python interpreter
	mainstr      string    // main function code string
	libext       string
	extraGccArgs string
	lang         int // c-python api version (2,3)
}

func (g *pyGen) gen() error {
	g.pkg = nil
	err := os.MkdirAll(g.odir, 0755)
	if err != nil {
		return fmt.Errorf("gopy: could not create output directory: %v", err)
	}

	g.genPre()
	g.genExtTypesGo()
	for _, p := range Packages {
		g.genPkg(p)
	}
	g.genOut()
	if len(g.err) == 0 {
		return nil
	}
	return g.err.Error()
}

func (g *pyGen) genPackageMap() {
	g.pkgmap = make(map[string]struct{})
	for _, p := range Packages {
		g.pkgmap[p.pkg.Path()] = struct{}{}
	}
}

func (g *pyGen) genPre() {
	g.gofile = &printer{buf: new(bytes.Buffer), indentEach: []byte("\t")}
	g.pybuild = &printer{buf: new(bytes.Buffer), indentEach: []byte("\t")}
	g.makefile = &printer{buf: new(bytes.Buffer), indentEach: []byte("\t")}
	g.genGoPreamble()
	g.genPyBuildPreamble()
	g.genMakefile()
	oinit, err := os.Create(filepath.Join(g.odir, "__init__.py"))
	g.err.Add(err)
	err = oinit.Close()
	g.err.Add(err)
}

func (g *pyGen) genPrintOut(outfn string, pr *printer) {
	of, err := os.Create(filepath.Join(g.odir, outfn))
	g.err.Add(err)
	_, err = io.Copy(of, pr)
	g.err.Add(err)
	err = of.Close()
	g.err.Add(err)
}

func (g *pyGen) genOut() {
	g.pybuild.Printf("\nmod.generate(open('%v.c', 'w'))\n\n", g.outname)
	g.gofile.Printf("\n\n")
	g.makefile.Printf("\n\n")
	g.genPrintOut(g.outname+".go", g.gofile)
	g.genPrintOut("build.py", g.pybuild)
	g.genPrintOut("Makefile", g.makefile)
}

func (g *pyGen) genPkgWrapOut() {
	g.pywrap.Printf("\n\n")
	g.genPrintOut(g.pkg.pkg.Name()+".py", g.pywrap)
}

func (g *pyGen) genPkg(p *Package) {
	g.pkg = p
	g.pywrap = &printer{buf: new(bytes.Buffer), indentEach: []byte("\t")}
	g.genPyWrapPreamble()
	if p == goPackage {
		g.genGoPkg()
		g.genExtTypesPyWrap()
		g.genPkgWrapOut()
	} else {
		g.genAll()
		g.genPkgWrapOut()
	}
	g.pkg = nil
}

func (g *pyGen) genGoPreamble() {
	pkgimport := ""
	for pi, _ := range current.imports {
		pkgimport += fmt.Sprintf("\n\t%q", pi)
	}
	pypath, pyonly := filepath.Split(g.vm)
	pyroot, _ := filepath.Split(filepath.Clean(pypath))
	libcfg := filepath.Join(filepath.Join(filepath.Join(pyroot, "lib"), "pkgconfig"), pyonly+".pc")
	if g.mode == ModeExe && g.mainstr == "" {
		g.mainstr = "GoPyMainRun()" // default is just to run main
	}
	exeprec := ""
	exeprego := ""
	if g.mode == ModeExe {
		exeprec = fmt.Sprintf(goExePreambleC, g.outname)
		exeprego = goExePreambleGo
	}
	g.gofile.Printf(goPreamble, g.outname, g.cmdstr, libcfg, GoHandle, CGoHandle, pkgimport, g.mainstr, exeprec, exeprego)
	g.gofile.Printf("\n// --- generated code for package: %[1]s below: ---\n\n", g.outname)
}

func (g *pyGen) genPyBuildPreamble() {
	g.pybuild.Printf(PyBuildPreamble, g.outname, g.cmdstr)
}

func (g *pyGen) genPyWrapPreamble() {
	n := g.pkg.pkg.Name()
	pkgimport := g.pkg.pkg.Path()
	pkgDoc := ""
	if g.pkg.doc != nil {
		pkgDoc = g.pkg.doc.Doc
	}
	if pkgDoc != "" {
		pkgDoc = `"""` + "\n" + pkgDoc + "\n" + `"""`
	}

	// import other packages for other types that we might use
	impstr := ""
	switch {
	case g.pkg.Name() == "go":
		impstr += fmt.Sprintf(GoPkgDefs, g.outname)
	case g.mode == ModeGen || g.mode == ModeBuild:
		impstr += fmt.Sprintf("import go\n")
	default:
		impstr += fmt.Sprintf("from %s import go\n", g.outname)
	}
	imps := g.pkg.pkg.Imports()
	for _, im := range imps {
		ipath := im.Path()
		if _, has := g.pkgmap[ipath]; has {
			if g.mode == ModeGen || g.mode == ModeBuild {
				impstr += fmt.Sprintf("import %s\n", im.Name())
			} else {
				impstr += fmt.Sprintf("from %s import %s\n", g.outname, im.Name())
			}
		}
	}

	if g.mode == ModeExe {
		g.pywrap.Printf(PyWrapExePreamble, g.outname, g.cmdstr, n, pkgimport, pkgDoc, impstr)
	} else {
		g.pywrap.Printf(PyWrapPreamble, g.outname, g.cmdstr, n, pkgimport, pkgDoc, impstr)
	}
}

// CmdStrToMakefile does what is needed to make the command string suitable for makefiles
// * removes -output
func CmdStrToMakefile(cmdstr string) string {
	if oidx := strings.Index(cmdstr, "-output="); oidx > 0 {
		spidx := strings.Index(cmdstr[oidx:], " ")
		cmdstr = cmdstr[:oidx] + cmdstr[oidx+spidx+1:]
	}
	return cmdstr
}

func (g *pyGen) genMakefile() {
	gencmd := strings.Replace(g.cmdstr, "gopy build", "gopy gen", 1)
	gencmd = CmdStrToMakefile(gencmd)
	if g.mode == ModeExe {
		g.makefile.Printf(MakefileExeTemplate, g.outname, g.cmdstr, gencmd, g.vm, g.libext)
	} else {
		g.makefile.Printf(MakefileTemplate, g.outname, g.cmdstr, gencmd, g.vm, g.libext, g.extraGccArgs)
	}
}

// generate external types, go code
func (g *pyGen) genExtTypesGo() {
	g.gofile.Printf("\n// ---- External Types Outside of Targeted Packages ---\n")

	names := current.names()
	for _, n := range names {
		sym := current.sym(n)
		if !sym.isType() {
			continue
		}
		if _, has := g.pkgmap[sym.gopkg.Path()]; has {
			continue
		}
		g.genType(sym, true, false) // ext types, no python wrapping
	}
}

// generate external types, py wrap
func (g *pyGen) genExtTypesPyWrap() {
	g.pywrap.Printf("\n# ---- External Types Outside of Targeted Packages ---\n")

	names := current.names()
	for _, n := range names {
		sym := current.sym(n)
		if !sym.isType() {
			continue
		}
		if _, has := g.pkgmap[sym.gopkg.Path()]; has {
			continue
		}
		g.genType(sym, true, true) // ext types, only python wrapping
	}
}

func (g *pyGen) genAll() {
	g.gofile.Printf("\n// ---- Package: %s ---\n", g.pkg.Name())

	g.gofile.Printf("\n// ---- Types ---\n")
	g.pywrap.Printf("\n# ---- Types ---\n")
	names := current.names()
	for _, n := range names {
		sym := current.sym(n)
		if sym.gopkg.Path() != g.pkg.pkg.Path() { // sometimes the package is not the same!!  yikes!
			continue
		}
		if !sym.isType() {
			continue
		}
		g.genType(sym, false, false) // not exttypes
	}

	g.pywrap.Printf("\n\n#---- Constants from Go: Python can only ask that you please don't change these! ---\n")
	for _, c := range g.pkg.consts {
		g.genConst(c)
	}

	g.gofile.Printf("\n\n// ---- Global Variables: can only use functions to access ---\n")
	g.pywrap.Printf("\n\n# ---- Global Variables: can only use functions to access ---\n")
	for _, v := range g.pkg.vars {
		g.genVar(v)
	}

	g.gofile.Printf("\n\n// ---- Interfaces ---\n")
	g.pywrap.Printf("\n\n# ---- Interfaces ---\n")
	for _, ifc := range g.pkg.ifaces {
		g.genInterface(ifc)
	}

	g.gofile.Printf("\n\n// ---- Structs ---\n")
	g.pywrap.Printf("\n\n# ---- Structs ---\n")
	g.pkg.sortStructEmbeds()
	for _, s := range g.pkg.structs {
		g.genStruct(s)
	}

	g.gofile.Printf("\n\n// ---- Slices ---\n")
	g.pywrap.Printf("\n\n# ---- Slices ---\n")
	for _, s := range g.pkg.slices {
		g.genSlice(s.sym, false, false, s)
	}

	g.gofile.Printf("\n\n// ---- Maps ---\n")
	g.pywrap.Printf("\n\n# ---- Maps ---\n")
	for _, m := range g.pkg.maps {
		g.genMap(m.sym, false, false, m)
	}

	// note: these are extracted from reg functions that return full
	// type (not pointer -- should do pointer but didn't work yet)
	g.gofile.Printf("\n\n// ---- Constructors ---\n")
	g.pywrap.Printf("\n\n# ---- Constructors ---\n")
	for _, s := range g.pkg.structs {
		for _, ctor := range s.ctors {
			g.genFunc(ctor)
		}
	}

	g.gofile.Printf("\n\n// ---- Functions ---\n")
	g.pywrap.Printf("\n\n# ---- Functions ---\n")
	for _, f := range g.pkg.funcs {
		g.genFunc(f)
	}
}

func (g *pyGen) genGoPkg() {
	g.gofile.Printf("\n// ---- Package: %s ---\n", g.pkg.Name())

	g.gofile.Printf("\n// ---- Types ---\n")
	g.pywrap.Printf("\n# ---- Types ---\n")
	names := universe.names()
	for _, n := range names {
		sym := universe.sym(n)
		if sym.gopkg == nil && sym.goname == "interface{}" {
			g.genType(sym, false, false)
			continue
		}
		if sym.gopkg == nil {
			continue
		}
		if !sym.isType() || sym.gopkg.Path() != g.pkg.pkg.Path() {
			continue
		}
		g.genType(sym, false, false) // not exttypes
	}
}
