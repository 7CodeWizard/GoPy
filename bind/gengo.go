// Copyright 2015 The go-python Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"fmt"
	"go/token"
	"go/types"
	"strings"
)

const (
	goPreamble = `// Package main is an autogenerated binder stub for package %[1]s.
// gopy gen -lang=go %[1]s
//
// File is generated by gopy gen. Do not edit.
package main

//#cgo pkg-config: %[2]s --cflags --libs
//#include <stdlib.h>
//#include <string.h>
//#include <complex.h>
import "C"

import (
	"fmt"
	"sync"
	"unsafe"

	%[3]s
)

var _ = unsafe.Pointer(nil)
var _ = fmt.Sprintf

// --- begin cgo helpers ---

//export _cgopy_GoString
func _cgopy_GoString(str *C.char) string {
	return C.GoString(str)
}

//export _cgopy_CString
func _cgopy_CString(s string) *C.char {
	return C.CString(s)
}

//export _cgopy_ErrorIsNil
func _cgopy_ErrorIsNil(err error) bool {
	return err == nil
}

//export _cgopy_ErrorString
func _cgopy_ErrorString(err error) *C.char {
	return C.CString(err.Error())
}

// --- end cgo helpers ---

// --- begin cref helpers ---

type cobject struct {
	ptr unsafe.Pointer
	cnt int32
}

// refs stores Go objects that have been passed to another language.
var refs struct {
	sync.Mutex
	next int32 // next reference number to use for Go object, always negative
	refs map[unsafe.Pointer]int32
	ptrs map[int32]cobject
}

//export cgopy_incref
func cgopy_incref(ptr unsafe.Pointer) {
	refs.Lock()
	num, ok := refs.refs[ptr]
	if ok {
		s := refs.ptrs[num]
		refs.ptrs[num] = cobject{s.ptr, s.cnt + 1}
	} else {
		num = refs.next
		refs.next--
		if refs.next > 0 {
			panic("refs.next underflow")
		}
		refs.refs[ptr] = num
		refs.ptrs[num] = cobject{ptr, 1}
	}
	refs.Unlock()
}

//export cgopy_decref
func cgopy_decref(ptr unsafe.Pointer) {
	refs.Lock()
	num, ok := refs.refs[ptr]
	if !ok {
		panic("cgopy: decref untracked object")
	}
	s := refs.ptrs[num]
	if s.cnt - 1 <= 0 {
		delete(refs.ptrs, num)
		delete(refs.refs, ptr)
		refs.Unlock()
		return
	}
	refs.ptrs[num] = cobject{s.ptr, s.cnt - 1}
	refs.Unlock()
}

func init() {
	refs.Lock()
	refs.next = -24 // Go objects get negative reference numbers. Arbitrary starting point.
	refs.refs = make(map[unsafe.Pointer]int32)
	refs.ptrs = make(map[int32]cobject)
	refs.Unlock()

	// make sure cgo is used and cgo hooks are run
	str := C.CString(%[1]q)
	C.free(unsafe.Pointer(str))
}

// --- end cref helpers ---
`
)

type goGen struct {
	*printer

	fset *token.FileSet
	pkg  *Package
	lang int // python's version API (2 or 3)
	err  ErrorList
}

func (g *goGen) gen() error {

	g.genPreamble()

	// create a Cgo hook for empty packages
	g.genPackage()

	// process slices, arrays, ...
	for _, n := range g.pkg.syms.names() {
		sym := g.pkg.syms.sym(n)
		if !sym.isType() {
			continue
		}
		g.genType(sym)
	}

	for _, s := range g.pkg.structs {
		g.genStruct(s)
	}

	// expose ctors at module level
	for _, s := range g.pkg.structs {
		for _, ctor := range s.ctors {
			g.genFunc(ctor)
		}
	}

	for _, f := range g.pkg.funcs {
		g.genFunc(f)
	}

	for _, c := range g.pkg.consts {
		g.genConst(c)
	}

	for _, v := range g.pkg.vars {
		g.genVar(v)
	}

	g.Printf("// buildmode=c-shared needs a 'main'\nfunc main() {}\n")
	if len(g.err) > 0 {
		return g.err
	}

	return nil
}

func (g *goGen) genPackage() {
	g.Printf("\n//export cgo_pkg_%[1]s_init\n", g.pkg.Name())
	g.Printf("func cgo_pkg_%[1]s_init() {}\n\n", g.pkg.Name())
}

func (g *goGen) genFunc(f Func) {
	sig := f.Signature()

	params := "(" + g.tupleString(sig.Params()) + ")"
	ret := " (" + g.tupleString(sig.Results()) + ") "

	//funcName := o.Name()
	g.Printf(`
//export cgo_func_%[1]s
// cgo_func_%[1]s wraps %[2]s.%[3]s
func cgo_func_%[1]s%[4]v%[5]v{
`,
		f.ID(),
		f.Package().Name(),
		f.GoName(),
		params,
		ret,
	)

	g.Indent()
	g.genFuncBody(f)
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *goGen) genFuncBody(f Func) {
	sig := f.Signature()
	results := sig.Results()
	for i := range results {
		if i > 0 {
			g.Printf(", ")
		}
		g.Printf("_gopy_%03d", i)
	}
	if len(results) > 0 {
		g.Printf(" := ")
	}

	g.Printf("%s.%s(", g.pkg.Name(), f.GoName())

	args := sig.Params()
	for i, arg := range args {
		tail := ""
		if i+1 < len(args) {
			tail = ", "
		}
		head := arg.Name()
		if arg.needWrap() {
			head = fmt.Sprintf(
				"*(*%s)(unsafe.Pointer(%s))",
				types.TypeString(
					arg.GoType(),
					func(*types.Package) string { return g.pkg.Name() },
				),
				arg.Name(),
			)
		}
		g.Printf("%s%s", head, tail)
	}
	g.Printf(")\n")

	if len(results) <= 0 {
		return
	}

	for i, res := range results {
		if !res.needWrap() {
			continue
		}
		g.Printf("cgopy_incref(unsafe.Pointer(&_gopy_%03d))\n", i)
	}

	g.Printf("return ")
	for i, res := range results {
		if i > 0 {
			g.Printf(", ")
		}
		// if needWrap(res.GoType()) {
		// 	g.Printf("")
		// }
		if res.needWrap() {
			g.Printf("%s(unsafe.Pointer(&", res.sym.cgoname)
		}
		g.Printf("_gopy_%03d", i)
		if res.needWrap() {
			g.Printf("))")
		}
	}
	g.Printf("\n")
}

func (g *goGen) genStruct(s Struct) {
	//fmt.Printf("obj: %#v\ntyp: %#v\n", obj, typ)
	typ := s.Struct()
	g.Printf("\n// --- wrapping %s ---\n\n", s.sym.gofmt())
	g.Printf("//export %[1]s\n", s.sym.cgoname)
	g.Printf("// %[1]s wraps %[2]s\n", s.sym.cgoname, s.sym.gofmt())
	g.Printf("type %[1]s unsafe.Pointer\n\n", s.sym.cgoname)

	for i := 0; i < typ.NumFields(); i++ {
		f := typ.Field(i)
		if !f.Exported() {
			continue
		}

		ft := f.Type()
		fsym := g.pkg.syms.symtype(ft)
		ftname := fsym.cgotypename()
		if needWrapType(ft) {
			ftname = fmt.Sprintf("cgo_type_%[1]s_field_%d", s.ID(), i+1)
			g.Printf("//export %s\n", ftname)
			g.Printf("type %s unsafe.Pointer\n\n", ftname)
		}

		// -- getter --

		g.Printf("//export cgo_func_%[1]s_getter_%[2]d\n", s.ID(), i+1)
		g.Printf("func cgo_func_%[1]s_getter_%[2]d(self cgo_type_%[1]s) %[3]s {\n",
			s.ID(), i+1,
			ftname,
		)
		g.Indent()
		g.Printf(
			"ret := (*%[1]s)(unsafe.Pointer(self))\n",
			s.sym.gofmt(),
		)

		if !fsym.isBasic() {
			g.Printf("cgopy_incref(unsafe.Pointer(&ret.%s))\n", f.Name())
			g.Printf("return %s(unsafe.Pointer(&ret.%s))\n", ftname, f.Name())
		} else {
			g.Printf("return ret.%s\n", f.Name())
		}
		g.Outdent()
		g.Printf("}\n\n")

		// -- setter --
		g.Printf("//export cgo_func_%[1]s_setter_%[2]d\n", s.ID(), i+1)
		g.Printf("func cgo_func_%[1]s_setter_%[2]d(self cgo_type_%[1]s, v %[3]s) {\n",
			s.ID(), i+1, ftname,
		)
		g.Indent()
		fset := "v"
		if !fsym.isBasic() {
			fset = fmt.Sprintf("*(*%s)(unsafe.Pointer(v))", fsym.gofmt())
		}
		g.Printf(
			"(*%[1]s)(unsafe.Pointer(self)).%[2]s = %[3]s\n",
			s.sym.gofmt(),
			f.Name(),
			fset,
		)
		g.Outdent()
		g.Printf("}\n\n")
	}

	for _, m := range s.meths {
		g.genMethod(s, m)
	}

	g.Printf("//export cgo_func_%[1]s_new\n", s.ID())
	g.Printf("func cgo_func_%[1]s_new() cgo_type_%[1]s {\n", s.ID())
	g.Indent()
	g.Printf("o := %[1]s{}\n", s.sym.gofmt())
	g.Printf("cgopy_incref(unsafe.Pointer(&o))\n")
	g.Printf("return (cgo_type_%[1]s)(unsafe.Pointer(&o))\n", s.ID())
	g.Outdent()
	g.Printf("}\n\n")

	// empty interface converter
	g.Printf("//export cgo_func_%[1]s_eface\n", s.ID())
	g.Printf("func cgo_func_%[1]s_eface(self %[2]s) interface{} {\n",
		s.sym.id,
		s.sym.cgoname,
	)
	g.Indent()
	g.Printf("var v interface{} = ")
	if s.sym.isBasic() {
		g.Printf("%[1]s(self)\n", s.sym.gofmt())
	} else {
		g.Printf("*(*%[1]s)(unsafe.Pointer(self))\n", s.sym.gofmt())
	}
	g.Printf("return v\n")
	g.Outdent()
	g.Printf("}\n\n")

	// support for __str__
	g.Printf("//export cgo_func_%[1]s_str\n", s.ID())
	g.Printf(
		"func cgo_func_%[1]s_str(self %[2]s) string {\n",
		s.ID(),
		s.sym.cgoname,
	)
	g.Indent()
	if (s.prots & ProtoStringer) == 0 {
		g.Printf("return fmt.Sprintf(\"%%#v\", ")
		g.Printf("*(*%[1]s)(unsafe.Pointer(self)))\n", s.sym.gofmt())
	} else {
		g.Printf("return (*%[1]s)(unsafe.Pointer(self)).String()\n",
			s.sym.gofmt(),
		)
	}
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *goGen) genMethod(s Struct, m Func) {
	sig := m.Signature()
	params := "(self cgo_type_" + s.ID()
	if len(sig.Params()) > 0 {
		params += ", " + g.tupleString(sig.Params())
	}
	params += ")"
	ret := " (" + g.tupleString(sig.Results()) + ") "

	g.Printf("//export cgo_func_%[1]s\n", m.ID())
	g.Printf("func cgo_func_%[1]s%[2]s%[3]s{\n",
		m.ID(),
		params,
		ret,
	)
	g.Indent()
	g.genMethodBody(s, m)
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *goGen) genMethodBody(s Struct, m Func) {
	sig := m.Signature()
	results := sig.Results()
	for i := range results {
		if i > 0 {
			g.Printf(", ")
		}
		g.Printf("_gopy_%03d", i)
	}
	if len(results) > 0 {
		g.Printf(" := ")
	}

	g.Printf("(*%s)(unsafe.Pointer(self)).%s(",
		s.sym.gofmt(),
		m.GoName(),
	)

	args := sig.Params()
	for i, arg := range args {
		tail := ""
		if i+1 < len(args) {
			tail = ", "
		}
		if arg.sym.isStruct() {
			g.Printf("*(*%s)(unsafe.Pointer(%s))%s", arg.sym.gofmt(), arg.Name(), tail)
		} else {
			g.Printf("%s%s", arg.Name(), tail)
		}
	}
	g.Printf(")\n")

	if len(results) <= 0 {
		return
	}

	for i, res := range results {
		if !res.needWrap() {
			continue
		}
		g.Printf("cgopy_incref(unsafe.Pointer(&_gopy_%03d))\n", i)
	}

	g.Printf("return ")
	for i, res := range results {
		if i > 0 {
			g.Printf(", ")
		}
		// if needWrap(res.GoType()) {
		// 	g.Printf("")
		// }
		if res.needWrap() {
			g.Printf("%s(unsafe.Pointer(&", res.sym.cgoname)
		}
		g.Printf("_gopy_%03d", i)
		if res.needWrap() {
			g.Printf("))")
		}
	}
	g.Printf("\n")

}

func (g *goGen) genConst(o Const) {
	sym := o.sym
	g.Printf("//export cgo_func_%s_get\n", o.id)
	g.Printf("func cgo_func_%[1]s_get() %[2]s {\n", o.id, sym.cgotypename())
	g.Indent()
	g.Printf("return %s(%s.%s)\n", sym.cgotypename(), o.pkg.Name(), o.obj.Name())
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *goGen) genVar(o Var) {
	pkgname := o.pkg.Name()
	typ := o.GoType()
	ret := o.sym.cgotypename()

	g.Printf("//export cgo_func_%s_get\n", o.id)
	g.Printf("func cgo_func_%[1]s_get() %[2]s {\n", o.id, ret)
	g.Indent()
	if o.needWrap() {
		g.Printf("cgopy_incref(unsafe.Pointer(&%s.%s))\n", pkgname, o.Name())
	}
	g.Printf("return ")
	if o.needWrap() {
		g.Printf("%s(unsafe.Pointer(&%s.%s))",
			ret, pkgname, o.Name(),
		)
	} else {
		g.Printf("%s(%s.%s)", ret, pkgname, o.Name())
	}
	g.Printf("\n")
	g.Outdent()
	g.Printf("}\n\n")

	g.Printf("//export cgo_func_%s_set\n", o.id)
	g.Printf("func cgo_func_%[1]s_set(v %[2]s) {\n", o.id, ret)
	g.Indent()
	vset := "v"
	if needWrapType(typ) {
		vset = fmt.Sprintf("*(*%s)(unsafe.Pointer(v))", o.sym.gofmt())
	} else {
		vset = fmt.Sprintf("%s(v)", o.sym.gofmt())
	}
	g.Printf(
		"%[1]s.%[2]s = %[3]s\n",
		pkgname, o.Name(), vset,
	)
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *goGen) genType(sym *symbol) {
	if !sym.isType() {
		return
	}
	if sym.isStruct() {
		return
	}
	if sym.isBasic() && !sym.isNamed() {
		return
	}

	g.Printf("\n// --- wrapping %s ---\n\n", sym.gofmt())
	g.Printf("//export %[1]s\n", sym.cgoname)
	g.Printf("// %[1]s wraps %[2]s\n", sym.cgoname, sym.gofmt())
	if sym.isBasic() {
		// we need to reach at the underlying type
		btyp := sym.GoType().Underlying().String()
		g.Printf("type %[1]s %[2]s\n\n", sym.cgoname, btyp)
	} else {
		g.Printf("type %[1]s unsafe.Pointer\n\n", sym.cgoname)
	}
	g.Printf("//export cgo_func_%[1]s_new\n", sym.id)
	g.Printf("func cgo_func_%[1]s_new() %[2]s {\n", sym.id, sym.cgoname)
	g.Indent()
	g.Printf("var o %[1]s\n", sym.gofmt())
	if sym.isBasic() {
		g.Printf("return %[1]s(o)\n", sym.cgoname)
	} else {
		g.Printf("cgopy_incref(unsafe.Pointer(&o))\n")
		g.Printf("return (%[1]s)(unsafe.Pointer(&o))\n", sym.cgoname)
	}
	g.Outdent()
	g.Printf("}\n\n")

	// empty interface converter
	g.Printf("//export cgo_func_%[1]s_eface\n", sym.id)
	g.Printf("func cgo_func_%[1]s_eface(self %[2]s) interface{} {\n",
		sym.id,
		sym.cgoname,
	)
	g.Indent()
	g.Printf("var v interface{} = ")
	if sym.isBasic() {
		g.Printf("%[1]s(self)\n", sym.gofmt())
	} else {
		g.Printf("*(*%[1]s)(unsafe.Pointer(self))\n", sym.gofmt())
	}
	g.Printf("return v\n")
	g.Outdent()
	g.Printf("}\n\n")

	// support for __str__
	g.Printf("//export cgo_func_%[1]s_str\n", sym.id)
	g.Printf(
		"func cgo_func_%[1]s_str(self %[2]s) string {\n",
		sym.id,
		sym.cgoname,
	)
	g.Indent()
	g.Printf("return fmt.Sprintf(\"%%#v\", ")
	if sym.isBasic() {
		g.Printf("%[1]s(self))\n", sym.gofmt())
	} else {
		g.Printf("*(*%[1]s)(unsafe.Pointer(self)))\n", sym.gofmt())
	}
	g.Outdent()
	g.Printf("}\n\n")

	if sym.isArray() || sym.isSlice() {
		var etyp types.Type
		switch typ := sym.GoType().(type) {
		case *types.Array:
			etyp = typ.Elem()
		case *types.Slice:
			etyp = typ.Elem()
		case *types.Named:
			switch typ := typ.Underlying().(type) {
			case *types.Array:
				etyp = typ.Elem()
			case *types.Slice:
				etyp = typ.Elem()
			default:
				panic(fmt.Errorf("gopy: unhandled type [%#v]", typ))
			}
		default:
			panic(fmt.Errorf("gopy: unhandled type [%#v]", typ))
		}
		esym := g.pkg.syms.symtype(etyp)
		if esym == nil {
			panic(fmt.Errorf("gopy: could not retrieve element type of %#v",
				sym,
			))
		}

		// support for __getitem__
		g.Printf("//export cgo_func_%[1]s_item\n", sym.id)
		g.Printf(
			"func cgo_func_%[1]s_item(self %[2]s, i int) %[3]s {\n",
			sym.id,
			sym.cgoname,
			esym.cgotypename(),
		)
		g.Indent()
		g.Printf("arr := (*%[1]s)(unsafe.Pointer(self))\n", sym.gofmt())
		g.Printf("elt := (*arr)[i]\n")
		if !esym.isBasic() {
			g.Printf("cgopy_incref(unsafe.Pointer(&elt))\n")
			g.Printf("return (%[1]s)(unsafe.Pointer(&elt))\n", esym.cgotypename())
		} else {
			if esym.isNamed() {
				g.Printf("return %[1]s(elt)\n", esym.cgotypename())
			} else {
				g.Printf("return elt\n")
			}
		}
		g.Outdent()
		g.Printf("}\n\n")

		// support for __setitem__
		g.Printf("//export cgo_func_%[1]s_ass_item\n", sym.id)
		g.Printf("func cgo_func_%[1]s_ass_item(self %[2]s, i int, v %[3]s) {\n",
			sym.id,
			sym.cgoname,
			esym.cgotypename(),
		)
		g.Indent()
		g.Printf("arr := (*%[1]s)(unsafe.Pointer(self))\n", sym.gofmt())
		g.Printf("(*arr)[i] = ")
		if !esym.isBasic() {
			g.Printf("*(*%[1]s)(unsafe.Pointer(v))\n", esym.gofmt())
		} else {
			if esym.isNamed() {
				g.Printf("%[1]s(v)\n", esym.gofmt())
			} else {
				g.Printf("v\n")
			}
		}
		g.Outdent()
		g.Printf("}\n\n")
	}

	if sym.isSlice() {
		etyp := sym.GoType().Underlying().(*types.Slice).Elem()
		esym := g.pkg.syms.symtype(etyp)
		if esym == nil {
			panic(fmt.Errorf("gopy: could not retrieve element type of %#v",
				sym,
			))
		}

		// support for __append__
		g.Printf("//export cgo_func_%[1]s_append\n", sym.id)
		g.Printf("func cgo_func_%[1]s_append(self %[2]s, v %[3]s) {\n",
			sym.id,
			sym.cgoname,
			esym.cgotypename(),
		)
		g.Indent()
		g.Printf("slice := (*%[1]s)(unsafe.Pointer(self))\n", sym.gofmt())
		g.Printf("*slice = append(*slice, ")
		if !esym.isBasic() {
			g.Printf("*(*%[1]s)(unsafe.Pointer(v))", esym.gofmt())
		} else {
			if esym.isNamed() {
				g.Printf("%[1]s(v)", esym.gofmt())
			} else {
				g.Printf("v")
			}
		}
		g.Printf(")\n")
		g.Outdent()
		g.Printf("}\n\n")
	}

	g.genTypeTPCall(sym)

	g.genTypeMethods(sym)

}

func (g *goGen) genTypeTPCall(sym *symbol) {
	if !sym.isSignature() {
		return
	}

	sig := sym.GoType().Underlying().(*types.Signature)
	if sig.Recv() != nil {
		// don't generate tp_call for methods.
		return
	}

	// support for __call__
	g.Printf("//export cgo_func_%[1]s_call\n", sym.id)
	g.Printf("func cgo_func_%[1]s_call(self %[2]s", sym.id, sym.cgotypename())
	params := sig.Params()
	res := sig.Results()
	if params != nil && params.Len() > 0 {
		for i := 0; i < params.Len(); i++ {
			arg := params.At(i)
			sarg := g.pkg.syms.symtype(arg.Type())
			if sarg == nil {
				panic(fmt.Errorf(
					"gopy: could not find symtype for [%T]",
					arg.Type(),
				))
			}
			g.Printf(", arg%03d %s", i, sarg.cgotypename())
		}
	}
	g.Printf(")")
	if res != nil && res.Len() > 0 {
		g.Printf(" (")
		for i := 0; i < res.Len(); i++ {
			ret := res.At(i)
			sret := g.pkg.syms.symtype(ret.Type())
			if sret == nil {
				panic(fmt.Errorf(
					"gopy: could not find symbol for [%T]",
					ret.Type(),
				))
			}
			comma := ", "
			if i == 0 {
				comma = ""
			}
			g.Printf("%s%s", comma, sret.cgotypename())
		}
		g.Printf(")")
	}
	g.Printf(" {\n")
	g.Indent()
	if res != nil && res.Len() > 0 {
		for i := 0; i < res.Len(); i++ {
			if i > 0 {
				g.Printf(", ")
			}
			g.Printf("res%03d", i)
		}
		g.Printf(" := ")
	}
	g.Printf("(*(*%[1]s)(unsafe.Pointer(self)))(", sym.gofmt())
	if params != nil && params.Len() > 0 {
		for i := 0; i < params.Len(); i++ {
			comma := ", "
			if i == 0 {
				comma = ""
			}
			arg := params.At(i)
			sarg := g.pkg.syms.symtype(arg.Type())
			if sarg.isBasic() {
				g.Printf("%sarg%03d", comma, i)
			} else {
				g.Printf(
					"%s*(*%s)(unsafe.Pointer(arg%03d))",
					comma,
					sarg.gofmt(),
					i,
				)
			}
		}
	}
	g.Printf(")\n")
	if res != nil && res.Len() > 0 {
		for i := 0; i < res.Len(); i++ {
			ret := res.At(i)
			sret := g.pkg.syms.symtype(ret.Type())
			if !needWrapType(sret.GoType()) {
				continue
			}
			g.Printf("cgopy_incref(unsafe.Pointer(&arg%03d))", i)
		}

		g.Printf("return ")
		for i := 0; i < res.Len(); i++ {
			if i > 0 {
				g.Printf(", ")
			}
			ret := res.At(i)
			sret := g.pkg.syms.symtype(ret.Type())
			if needWrapType(ret.Type()) {
				g.Printf("%s(unsafe.Pointer(&", sret.cgotypename())
			}
			g.Printf("res%03d", i)
			if needWrapType(ret.Type()) {
				g.Printf("))")
			}
		}
		g.Printf("\n")
	}
	g.Outdent()
	g.Printf("}\n\n")

}

func (g *goGen) genTypeMethods(sym *symbol) {
	if !sym.isNamed() {
		return
	}

	typ := sym.GoType().(*types.Named)
	for imeth := 0; imeth < typ.NumMethods(); imeth++ {
		m := typ.Method(imeth)
		if !m.Exported() {
			continue
		}

		mname := types.ObjectString(m, nil)
		msym := g.pkg.syms.sym(mname)
		if msym == nil {
			panic(fmt.Errorf(
				"gopy: could not find symbol for [%[1]T] (%#[1]v) (%[2]s)",
				m.Type(),
				m.Name()+" || "+m.FullName(),
			))
		}
		g.Printf("//export cgo_func_%[1]s\n", msym.id)
		g.Printf("func cgo_func_%[1]s(self %[2]s",
			msym.id,
			sym.cgoname,
		)
		sig := m.Type().(*types.Signature)
		params := sig.Params()
		if params != nil {
			for i := 0; i < params.Len(); i++ {
				arg := params.At(i)
				sarg := g.pkg.syms.symtype(arg.Type())
				if sarg == nil {
					panic(fmt.Errorf(
						"gopy: could not find symbol for [%T]",
						arg.Type(),
					))
				}
				g.Printf(", arg%03d %s", i, sarg.cgotypename())
			}
		}
		g.Printf(") ")
		res := sig.Results()
		if res != nil {
			g.Printf("(")
			for i := 0; i < res.Len(); i++ {
				if i > 0 {
					g.Printf(", ")
				}
				ret := res.At(i)
				sret := g.pkg.syms.symtype(ret.Type())
				if sret == nil {
					panic(fmt.Errorf(
						"gopy: could not find symbol for [%T]",
						ret.Type(),
					))
				}
				g.Printf("%s", sret.cgotypename())
			}
			g.Printf(")")
		}
		g.Printf(" {\n")
		g.Indent()

		if res != nil {
			for i := 0; i < res.Len(); i++ {
				if i > 0 {
					g.Printf(", ")
				}
				g.Printf("res%03d", i)
			}
			if res.Len() > 0 {
				g.Printf(" := ")
			}
		}
		if sym.isBasic() {
			g.Printf("(*%s)(unsafe.Pointer(&self)).%s(",
				sym.gofmt(),
				msym.goname,
			)
		} else {
			g.Printf("(*%s)(unsafe.Pointer(self)).%s(",
				sym.gofmt(),
				msym.goname,
			)
		}

		if params != nil {
			for i := 0; i < params.Len(); i++ {
				if i > 0 {
					g.Printf(", ")
				}
				sarg := g.pkg.syms.symtype(params.At(i).Type())
				if needWrapType(sarg.GoType()) {
					g.Printf("*(*%s)(unsafe.Pointer(arg%03d))",
						sarg.gofmt(),
						i,
					)
				} else {
					g.Printf("arg%03d", i)
				}
			}
		}
		g.Printf(")\n")

		if res == nil || res.Len() <= 0 {
			g.Outdent()
			g.Printf("}\n\n")
			continue
		}

		g.Printf("return ")
		for i := 0; i < res.Len(); i++ {
			if i > 0 {
				g.Printf(", ")
			}
			sret := g.pkg.syms.symtype(res.At(i).Type())
			if needWrapType(sret.GoType()) {
				g.Printf(
					"%s(unsafe.Pointer(&",
					sret.cgoname,
				)
			}
			g.Printf("res%03d", i)
			if needWrapType(sret.GoType()) {
				g.Printf("))")
			}
		}
		g.Printf("\n")

		g.Outdent()
		g.Printf("}\n\n")
	}
}

func (g *goGen) genPreamble() {
	n := g.pkg.pkg.Name()
	pkgimport := fmt.Sprintf("%q", g.pkg.pkg.Path())
	if g.pkg.n == 0 {
		pkgimport = fmt.Sprintf("_ %q", g.pkg.pkg.Path())
	}

	pkgcfg, err := getPkgConfig(g.lang)
	if err != nil {
		panic(err)
	}

	g.Printf(goPreamble, n, pkgcfg, pkgimport)
}

func (g *goGen) tupleString(tuple []*Var) string {
	n := len(tuple)
	if n <= 0 {
		return ""
	}

	str := make([]string, 0, n)
	for _, v := range tuple {
		n := v.Name()
		//typ := v.GoType()
		sym := v.sym
		//str = append(str, n+" "+qualifiedType(typ))
		tname := sym.cgotypename()
		str = append(str, n+" "+tname)
	}

	return strings.Join(str, ", ")
}
