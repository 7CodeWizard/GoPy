// Copyright 2015 The go-python Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"fmt"
	"go/token"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/types"
)

const (
	cPreamble = `/*
  C stubs for package %[1]s.
  gopy gen -lang=python %[1]s

  File is generated by gopy gen. Do not edit.
*/

#ifdef _POSIX_C_SOURCE
#undef _POSIX_C_SOURCE
#endif

#include "Python.h"
#include "structmember.h"

// header exported from 'go tool cgo'
#include "%[3]s.h"

// helpers for cgopy

static int
cgopy_cnv_py2c_bool(PyObject *o, GoUint8 *addr) {
	*addr = (o == Py_True) ? 1 : 0;
	return 1;
}

static PyObject*
cgopy_cnv_c2py_bool(GoUint8 *addr) {
	long v = *addr;
	return PyBool_FromLong(v);
}

static int
cgopy_cnv_py2c_string(PyObject *o, GoString *addr) {
	const char *str = PyString_AsString(o);
	if (str == NULL) {
		return 0;
	}
	*addr = _cgopy_GoString((char*)str);
	return 1;
}

static PyObject*
cgopy_cnv_c2py_string(GoString *addr) {
	const char *str = _cgopy_CString(*addr);
	PyObject *pystr = PyString_FromString(str);
	free((void*)str);
	return pystr;
}
`
)

type cpyGen struct {
	decl *printer
	impl *printer

	fset *token.FileSet
	pkg  *Package
	err  ErrorList
}

func (g *cpyGen) gen() error {

	g.genPreamble()

	// first, process slices, arrays
	{
		names := g.pkg.syms.names()
		for _, n := range names {
			sym := g.pkg.syms.sym(n)
			if !sym.isType() {
				continue
			}
			g.genType(sym)
		}
	}

	// then, process structs
	for _, s := range g.pkg.structs {
		g.genStruct(s)
	}

	// expose ctors at module level
	// FIXME(sbinet): attach them to structs?
	// -> problem is if one has 2 or more ctors with exactly the same signature.
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

	g.impl.Printf("static PyMethodDef cpy_%s_methods[] = {\n", g.pkg.pkg.Name())
	g.impl.Indent()
	for _, f := range g.pkg.funcs {
		name := f.GoName()
		//obj := scope.Lookup(name)
		g.impl.Printf("{%[1]q, %[2]s, METH_VARARGS, %[3]q},\n",
			name, "cpy_func_"+f.ID(), f.Doc(),
		)
	}
	// expose ctors at module level
	// FIXME(sbinet): attach them to structs?
	// -> problem is if one has 2 or more ctors with exactly the same signature.
	for _, s := range g.pkg.structs {
		for _, f := range s.ctors {
			name := f.GoName()
			//obj := scope.Lookup(name)
			g.impl.Printf("{%[1]q, %[2]s, METH_VARARGS, %[3]q},\n",
				name, "cpy_func_"+f.ID(), f.Doc(),
			)
		}
	}

	for _, c := range g.pkg.consts {
		name := c.GoName()
		g.impl.Printf("{%[1]q, %[2]s, METH_VARARGS, %[3]q},\n",
			"Get"+name, "cpy_func_"+c.id+"_get", c.Doc(),
		)
	}

	for _, v := range g.pkg.vars {
		name := v.Name()
		g.impl.Printf("{%[1]q, %[2]s, METH_VARARGS, %[3]q},\n",
			"Get"+name, "cpy_func_"+v.id+"_get", v.doc,
		)
		g.impl.Printf("{%[1]q, %[2]s, METH_VARARGS, %[3]q},\n",
			"Set"+name, "cpy_func_"+v.id+"_set", v.doc,
		)
	}

	g.impl.Printf("{NULL, NULL, 0, NULL}        /* Sentinel */\n")
	g.impl.Outdent()
	g.impl.Printf("};\n\n")

	g.impl.Printf("PyMODINIT_FUNC\ninit%[1]s(void)\n{\n", g.pkg.pkg.Name())
	g.impl.Indent()
	g.impl.Printf("PyObject *module = NULL;\n\n")

	for _, s := range g.pkg.structs {
		g.impl.Printf(
			"if (PyType_Ready(&%sType) < 0) { return; }\n",
			s.sym.cpyname,
		)
	}

	g.impl.Printf("module = Py_InitModule3(%[1]q, cpy_%[1]s_methods, %[2]q);\n\n",
		g.pkg.pkg.Name(),
		g.pkg.doc.Doc,
	)

	for _, s := range g.pkg.structs {
		g.impl.Printf("Py_INCREF(&%sType);\n", s.sym.cpyname)
		g.impl.Printf("PyModule_AddObject(module, %q, (PyObject*)&%sType);\n\n",
			s.GoName(),
			s.sym.cpyname,
		)
	}
	g.impl.Outdent()
	g.impl.Printf("}\n\n")

	if len(g.err) > 0 {
		return g.err
	}

	return nil
}

func (g *cpyGen) genFunc(o Func) {

	g.impl.Printf(`
/* pythonization of: %[1]s.%[2]s */
static PyObject*
cpy_func_%[3]s(PyObject *self, PyObject *args) {
`,
		g.pkg.pkg.Name(),
		o.GoName(),
		o.ID(),
	)

	g.impl.Indent()
	g.genFuncBody(o)
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genFuncBody(f Func) {
	id := f.ID()
	sig := f.Signature()

	funcArgs := []string{}

	res := sig.Results()
	args := sig.Params()
	var recv *Var
	if sig.Recv() != nil {
		recv = sig.Recv()
		recv.genRecvDecl(g.impl)
		funcArgs = append(funcArgs, recv.getFuncArg())
	}

	for _, arg := range args {
		arg.genDecl(g.impl)
		funcArgs = append(funcArgs, arg.getFuncArg())
	}

	if len(res) > 0 {
		switch len(res) {
		case 1:
			ret := res[0]
			ret.genRetDecl(g.impl)
		default:
			g.impl.Printf("struct cgo_func_%[1]s_return c_gopy_ret;\n", id)
		}
	}

	g.impl.Printf("\n")

	if recv != nil {
		recv.genRecvImpl(g.impl)
	}

	if len(args) > 0 {
		g.impl.Printf("if (!PyArg_ParseTuple(args, ")
		format := []string{}
		pyaddrs := []string{}
		for _, arg := range args {
			pyfmt, addr := arg.getArgParse()
			format = append(format, pyfmt)
			pyaddrs = append(pyaddrs, addr...)
		}
		g.impl.Printf("%q, %s)) {\n", strings.Join(format, ""), strings.Join(pyaddrs, ", "))
		g.impl.Indent()
		g.impl.Printf("return NULL;\n")
		g.impl.Outdent()
		g.impl.Printf("}\n\n")
	}

	if len(args) > 0 {
		for _, arg := range args {
			arg.genFuncPreamble(g.impl)
		}
		g.impl.Printf("\n")
	}

	if len(res) > 0 {
		g.impl.Printf("c_gopy_ret = ")
	}
	g.impl.Printf("cgo_func_%[1]s(%[2]s);\n", id, strings.Join(funcArgs, ", "))

	g.impl.Printf("\n")

	if len(res) <= 0 {
		g.impl.Printf("Py_INCREF(Py_None);\nreturn Py_None;\n")
		return
	}

	if f.err {
		switch len(res) {
		case 1:
			g.impl.Printf("if (!_cgopy_ErrorIsNil(c_gopy_ret)) {\n")
			g.impl.Indent()
			g.impl.Printf("const char* c_err_str = _cgopy_ErrorString(c_gopy_ret);\n")
			g.impl.Printf("PyErr_SetString(PyExc_RuntimeError, c_err_str);\n")
			g.impl.Printf("free((void*)c_err_str);\n")
			g.impl.Printf("return NULL;\n")
			g.impl.Outdent()
			g.impl.Printf("}\n\n")
			g.impl.Printf("Py_INCREF(Py_None);\nreturn Py_None;\n")
			return

		case 2:
			g.impl.Printf("if (!_cgopy_ErrorIsNil(c_gopy_ret.r1)) {\n")
			g.impl.Indent()
			g.impl.Printf("const char* c_err_str = _cgopy_ErrorString(c_gopy_ret.r1);\n")
			g.impl.Printf("PyErr_SetString(PyExc_RuntimeError, c_err_str);\n")
			g.impl.Printf("free((void*)c_err_str);\n")
			g.impl.Printf("return NULL;\n")
			g.impl.Outdent()
			g.impl.Printf("}\n\n")
			if f.ctor {
				ret := res[0]
				g.impl.Printf("PyObject *o = cpy_func_%[1]s_new(&%[2]sType, 0, 0);\n",
					ret.sym.id,
					ret.sym.cpyname,
				)
				g.impl.Printf("if (o == NULL) {\n")
				g.impl.Indent()
				g.impl.Printf("return NULL;\n")
				g.impl.Outdent()
				g.impl.Printf("}\n")
				g.impl.Printf("((%[1]s*)o)->cgopy = c_gopy_ret.r0;\n",
					ret.sym.cpyname,
				)
				g.impl.Printf("return o;\n")
				return
			}
			pyfmt, _ := res[0].getArgBuildValue()
			g.impl.Printf("return Py_BuildValue(%q, c_gopy_ret.r0);\n", pyfmt)
			return

		default:
			panic(fmt.Errorf(
				"bind: function/method with more than 2 results not supported! (%s)",
				f.ID(),
			))
		}
	}

	if f.ctor {
		ret := res[0]
		g.impl.Printf("PyObject *o = cpy_func_%[1]s_new(&%[2]sType, 0, 0);\n",
			ret.sym.id,
			ret.sym.cpyname,
		)
		g.impl.Printf("if (o == NULL) {\n")
		g.impl.Indent()
		g.impl.Printf("return NULL;\n")
		g.impl.Outdent()
		g.impl.Printf("}\n")
		g.impl.Printf("((%[1]s*)o)->cgopy = c_gopy_ret;\n",
			ret.sym.cpyname,
		)
		g.impl.Printf("return o;\n")
		return
	}

	format := []string{}
	funcArgs = []string{}
	switch len(res) {
	case 1:
		ret := res[0]
		ret.name = "gopy_ret"
		pyfmt, pyaddrs := ret.getArgBuildValue()
		format = append(format, pyfmt)
		funcArgs = append(funcArgs, pyaddrs...)
	default:
		for _, ret := range res {
			pyfmt, pyaddrs := ret.getArgBuildValue()
			format = append(format, pyfmt)
			funcArgs = append(funcArgs, pyaddrs...)
		}
	}

	g.impl.Printf("return Py_BuildValue(%q, %s);\n",
		strings.Join(format, ""),
		strings.Join(funcArgs, ", "),
	)
}

func (g *cpyGen) genStruct(cpy Struct) {
	pkgname := cpy.Package().Name()

	//fmt.Printf("obj: %#v\ntyp: %#v\n", obj, typ)
	g.decl.Printf("/* --- decls for struct %s.%v --- */\n", pkgname, cpy.GoName())
	g.decl.Printf("typedef void* %s;\n\n", cpy.sym.cgoname)
	g.decl.Printf("/* type for struct %s.%v\n", pkgname, cpy.GoName())
	g.decl.Printf(" */\ntypedef struct {\n")
	g.decl.Indent()
	g.decl.Printf("PyObject_HEAD\n")
	g.decl.Printf("%[1]s cgopy; /* unsafe.Pointer to %[2]s */\n",
		cpy.sym.cgoname,
		cpy.ID(),
	)
	g.decl.Outdent()
	g.decl.Printf("} %s;\n", cpy.sym.cpyname)
	g.decl.Printf("\n\n")

	g.impl.Printf("\n\n/* --- impl for %s.%v */\n\n", pkgname, cpy.GoName())

	g.genStructNew(cpy)
	g.genStructDealloc(cpy)
	g.genStructInit(cpy)
	g.genStructMembers(cpy)
	g.genStructMethods(cpy)

	g.genStructProtocols(cpy)

	g.impl.Printf("static PyTypeObject %sType = {\n", cpy.sym.cpyname)
	g.impl.Indent()
	g.impl.Printf("PyObject_HEAD_INIT(NULL)\n")
	g.impl.Printf("0,\t/*ob_size*/\n")
	g.impl.Printf("\"%s.%s\",\t/*tp_name*/\n", pkgname, cpy.GoName())
	g.impl.Printf("sizeof(%s),\t/*tp_basicsize*/\n", cpy.sym.cpyname)
	g.impl.Printf("0,\t/*tp_itemsize*/\n")
	g.impl.Printf("(destructor)%s_dealloc,\t/*tp_dealloc*/\n", cpy.sym.cpyname)
	g.impl.Printf("0,\t/*tp_print*/\n")
	g.impl.Printf("0,\t/*tp_getattr*/\n")
	g.impl.Printf("0,\t/*tp_setattr*/\n")
	g.impl.Printf("0,\t/*tp_compare*/\n")
	g.impl.Printf("0,\t/*tp_repr*/\n")
	g.impl.Printf("0,\t/*tp_as_number*/\n")
	g.impl.Printf("0,\t/*tp_as_sequence*/\n")
	g.impl.Printf("0,\t/*tp_as_mapping*/\n")
	g.impl.Printf("0,\t/*tp_hash */\n")
	g.impl.Printf("0,\t/*tp_call*/\n")
	g.impl.Printf("cpy_func_%s_tp_str,\t/*tp_str*/\n", cpy.sym.id)
	g.impl.Printf("0,\t/*tp_getattro*/\n")
	g.impl.Printf("0,\t/*tp_setattro*/\n")
	g.impl.Printf("0,\t/*tp_as_buffer*/\n")
	g.impl.Printf("Py_TPFLAGS_DEFAULT,\t/*tp_flags*/\n")
	g.impl.Printf("%q,\t/* tp_doc */\n", cpy.Doc())
	g.impl.Printf("0,\t/* tp_traverse */\n")
	g.impl.Printf("0,\t/* tp_clear */\n")
	g.impl.Printf("0,\t/* tp_richcompare */\n")
	g.impl.Printf("0,\t/* tp_weaklistoffset */\n")
	g.impl.Printf("0,\t/* tp_iter */\n")
	g.impl.Printf("0,\t/* tp_iternext */\n")
	g.impl.Printf("%s_methods,             /* tp_methods */\n", cpy.sym.cpyname)
	g.impl.Printf("0,\t/* tp_members */\n")
	g.impl.Printf("%s_getsets,\t/* tp_getset */\n", cpy.sym.cpyname)
	g.impl.Printf("0,\t/* tp_base */\n")
	g.impl.Printf("0,\t/* tp_dict */\n")
	g.impl.Printf("0,\t/* tp_descr_get */\n")
	g.impl.Printf("0,\t/* tp_descr_set */\n")
	g.impl.Printf("0,\t/* tp_dictoffset */\n")
	g.impl.Printf("(initproc)cpy_func_%s_init,      /* tp_init */\n", cpy.sym.id)
	g.impl.Printf("0,                         /* tp_alloc */\n")
	g.impl.Printf("cpy_func_%s_new,\t/* tp_new */\n", cpy.sym.id)
	g.impl.Outdent()
	g.impl.Printf("};\n\n")

	g.genStructConverters(cpy)

}

func (g *cpyGen) genStructNew(cpy Struct) {
	g.genTypeNew(cpy.sym)
}

func (g *cpyGen) genStructDealloc(cpy Struct) {
	g.genTypeDealloc(cpy.sym)
}

func (g *cpyGen) genStructInit(cpy Struct) {
	pkgname := cpy.Package().Name()

	g.decl.Printf("/* tp_init for %s.%v */\n", pkgname, cpy.GoName())
	g.decl.Printf(
		"static int\ncpy_func_%[1]s_init(%[2]s *self, PyObject *args, PyObject *kwds);\n",
		cpy.sym.id,
		cpy.sym.cpyname,
	)

	g.impl.Printf("/* tp_init */\n")
	g.impl.Printf(
		"static int\ncpy_func_%[1]s_init(%[2]s *self, PyObject *args, PyObject *kwds) {\n",
		cpy.sym.id,
		cpy.sym.cpyname,
	)
	g.impl.Indent()

	kwds := make(map[string]int)
	for _, ctor := range cpy.ctors {
		sig := ctor.Signature()
		for _, arg := range sig.Params() {
			n := arg.Name()
			if _, dup := kwds[n]; !dup {
				kwds[n] = len(kwds)
			}
		}
	}
	g.impl.Printf("static char *kwlist[] = {\n")
	g.impl.Indent()
	for k, v := range kwds {
		g.impl.Printf("%q, /* py_kwd_%d */\n", k, v)
	}
	g.impl.Printf("NULL\n")
	g.impl.Outdent()
	g.impl.Printf("};\n")

	for _, v := range kwds {
		g.impl.Printf("PyObject *py_kwd_%d = NULL;\n", v)
	}

	// FIXME(sbinet) remove when/if we manage to work out a proper dispatch
	// for ctors.
	g.impl.Printf("Py_ssize_t nkwds = (kwds != NULL) ? PyDict_Size(kwds) : 0;\n")
	g.impl.Printf("Py_ssize_t nargs = (args != NULL) ? PySequence_Size(args) : 0;\n")
	g.impl.Printf("if ((nkwds + nargs) > 0) {\n")
	g.impl.Indent()
	g.impl.Printf("PyErr_SetString(PyExc_TypeError, ")
	g.impl.Printf("\"%s.__init__ takes no argument\");\n", cpy.GoName())
	g.impl.Printf("return -1;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")

	g.impl.Printf("return 0;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genStructMembers(cpy Struct) {
	pkgname := cpy.Package().Name()
	typ := cpy.Struct()

	g.decl.Printf("/* tp_getset for %s.%v */\n", pkgname, cpy.GoName())
	for i := 0; i < typ.NumFields(); i++ {
		f := typ.Field(i)
		if !f.Exported() {
			continue
		}
		g.genStructMemberGetter(cpy, i, f)
		g.genStructMemberSetter(cpy, i, f)
	}

	g.impl.Printf("/* tp_getset for %s.%v */\n", pkgname, cpy.GoName())
	g.impl.Printf("static PyGetSetDef %s_getsets[] = {\n", cpy.sym.cpyname)
	g.impl.Indent()
	for i := 0; i < typ.NumFields(); i++ {
		f := typ.Field(i)
		if !f.Exported() {
			continue
		}
		doc := "doc for " + f.Name() // FIXME(sbinet) retrieve doc for fields
		g.impl.Printf("{%q, ", f.Name())
		g.impl.Printf("(getter)cpy_func_%[1]s_getter_%[2]d, ", cpy.sym.id, i+1)
		g.impl.Printf("(setter)cpy_func_%[1]s_setter_%[2]d, ", cpy.sym.id, i+1)
		g.impl.Printf("%q, NULL},\n", doc)
	}
	g.impl.Printf("{NULL} /* Sentinel */\n")
	g.impl.Outdent()
	g.impl.Printf("};\n\n")
}

func (g *cpyGen) genStructMemberGetter(cpy Struct, i int, f types.Object) {
	pkg := cpy.Package()
	ft := f.Type()
	var (
		cgo_fgetname = fmt.Sprintf("cgo_func_%[1]s_getter_%[2]d", cpy.sym.id, i+1)
		cpy_fgetname = fmt.Sprintf("cpy_func_%[1]s_getter_%[2]d", cpy.sym.id, i+1)
		ifield       = newVar(pkg, ft, f.Name(), "ret", "")
		results      = []*Var{ifield}
	)

	if needWrapType(ft) {
		g.decl.Printf("/* wrapper for field %s.%s.%s */\n",
			pkg.Name(),
			cpy.GoName(),
			f.Name(),
		)
		g.decl.Printf("typedef void* %[1]s_field_%d;\n", cpy.sym.cgoname, i+1)
	}

	g.decl.Printf("static PyObject*\n")
	g.decl.Printf(
		"%[2]s(%[1]s *self, void *closure); /* %[3]s */\n",
		cpy.sym.cpyname,
		cpy_fgetname,
		f.Name(),
	)

	g.impl.Printf("static PyObject*\n")
	g.impl.Printf(
		"%[2]s(%[1]s *self, void *closure) /* %[3]s */ {\n",
		cpy.sym.cpyname,
		cpy_fgetname,
		f.Name(),
	)
	g.impl.Indent()

	g.impl.Printf("PyObject *o = NULL;\n")
	ftname := g.pkg.syms.symtype(ft).cgoname
	if needWrapType(ft) {
		ftname = fmt.Sprintf("%[1]s_field_%d", cpy.sym.cgoname, i+1)
	}
	g.impl.Printf(
		"%[1]s c_ret = %[2]s(self->cgopy); /*wrap*/\n",
		ftname,
		cgo_fgetname,
	)

	{
		format := []string{}
		funcArgs := []string{}
		switch len(results) {
		case 1:
			ret := results[0]
			ret.name = "ret"
			pyfmt, pyaddrs := ret.getArgBuildValue()
			format = append(format, pyfmt)
			funcArgs = append(funcArgs, pyaddrs...)
		default:
			panic("bind: impossible")
		}
		g.impl.Printf("o = Py_BuildValue(%q, %s);\n",
			strings.Join(format, ""),
			strings.Join(funcArgs, ", "),
		)
	}

	g.impl.Printf("return o;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")

}

func (g *cpyGen) genStructMemberSetter(cpy Struct, i int, f types.Object) {
	var (
		pkg          = cpy.Package()
		ft           = f.Type()
		self         = newVar(pkg, cpy.GoType(), cpy.GoName(), "self", "")
		ifield       = newVar(pkg, ft, f.Name(), "ret", "")
		cgo_fsetname = fmt.Sprintf("cgo_func_%[1]s_setter_%[2]d", cpy.sym.id, i+1)
		cpy_fsetname = fmt.Sprintf("cpy_func_%[1]s_setter_%[2]d", cpy.sym.id, i+1)
	)

	g.decl.Printf("static int\n")
	g.decl.Printf(
		"%[2]s(%[1]s *self, PyObject *value, void *closure);\n",
		cpy.sym.cpyname,
		cpy_fsetname,
	)

	g.impl.Printf("static int\n")
	g.impl.Printf(
		"%[2]s(%[1]s *self, PyObject *value, void *closure) {\n",
		cpy.sym.cpyname,
		cpy_fsetname,
	)
	g.impl.Indent()

	ifield.genDecl(g.impl)
	g.impl.Printf("PyObject *tuple = NULL;\n\n")
	g.impl.Printf("if (value == NULL) {\n")
	g.impl.Indent()
	g.impl.Printf(
		"PyErr_SetString(PyExc_TypeError, \"Cannot delete '%[1]s' attribute\");\n",
		f.Name(),
	)
	g.impl.Printf("return -1;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n")

	// TODO(sbinet) check 'value' type (PyString_Check, PyInt_Check, ...)

	g.impl.Printf("tuple = PyTuple_New(1);\n")
	g.impl.Printf("Py_INCREF(value);\n")
	g.impl.Printf("PyTuple_SET_ITEM(tuple, 0, value);\n\n")

	g.impl.Printf("\nif (!PyArg_ParseTuple(tuple, ")
	pyfmt, pyaddr := ifield.getArgParse()
	g.impl.Printf("%q, %s)) {\n", pyfmt, strings.Join(pyaddr, ", "))
	g.impl.Indent()
	g.impl.Printf("Py_DECREF(tuple);\n")
	g.impl.Printf("return -1;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n")
	g.impl.Printf("Py_DECREF(tuple);\n\n")

	g.impl.Printf("%[1]s((%[2]s)(self->cgopy), c_%[3]s);\n",
		cgo_fsetname,
		self.CGoType(),
		ifield.Name(),
	)

	g.impl.Printf("return 0;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genStructMethods(cpy Struct) {

	pkgname := cpy.Package().Name()

	g.decl.Printf("/* methods for %s.%s */\n\n", pkgname, cpy.GoName())
	for _, m := range cpy.meths {
		g.genMethod(cpy, m)
	}

	g.impl.Printf("static PyMethodDef %s_methods[] = {\n", cpy.sym.cpyname)
	g.impl.Indent()
	for _, m := range cpy.meths {
		margs := "METH_VARARGS"
		if len(m.Signature().Params()) == 0 {
			margs = "METH_NOARGS"
		}
		g.impl.Printf(
			"{%[1]q, (PyCFunction)cpy_func_%[2]s, %[3]s, %[4]q},\n",
			m.GoName(),
			m.ID(),
			margs,
			m.Doc(),
		)
	}
	g.impl.Printf("{NULL} /* sentinel */\n")
	g.impl.Outdent()
	g.impl.Printf("};\n\n")
}

func (g *cpyGen) genMethod(cpy Struct, fct Func) {
	pkgname := g.pkg.pkg.Name()
	g.decl.Printf("/* wrapper of %[1]s.%[2]s */\n",
		pkgname,
		cpy.GoName()+"."+fct.GoName(),
	)
	g.decl.Printf("static PyObject*\n")
	g.decl.Printf("cpy_func_%s(PyObject *self, PyObject *args);\n", fct.ID())

	g.impl.Printf("/* wrapper of %[1]s.%[2]s */\n",
		pkgname,
		cpy.GoName()+"."+fct.GoName(),
	)
	g.impl.Printf("static PyObject*\n")
	g.impl.Printf("cpy_func_%s(PyObject *self, PyObject *args) {\n", fct.ID())
	g.impl.Indent()
	g.genMethodBody(fct)
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genMethodBody(fct Func) {
	g.genFuncBody(fct)
}

func (g *cpyGen) genStructProtocols(cpy Struct) {
	g.genStructTPStr(cpy)
}

func (g *cpyGen) genStructTPStr(cpy Struct) {
	g.decl.Printf(
		"static PyObject*\ncpy_func_%s_tp_str(PyObject *self);\n",
		cpy.sym.id,
	)

	g.impl.Printf(
		"static PyObject*\ncpy_func_%s_tp_str(PyObject *self) {\n",
		cpy.sym.id,
	)

	if (cpy.prots & ProtoStringer) == 0 {
		g.impl.Indent()
		g.impl.Printf("return PyObject_Repr(self);\n")
		g.impl.Outdent()
		g.impl.Printf("}\n\n")
		return
	}

	var m Func
	for _, f := range cpy.meths {
		if f.GoName() == "String" {
			m = f
			break
		}
	}

	g.impl.Indent()
	g.impl.Printf("return cpy_func_%[1]s(self, 0);\n", m.ID())
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genStructConverters(cpy Struct) {
	g.genTypeConverter(cpy.sym)
}

func (g *cpyGen) genConst(o Const) {
	g.genFunc(o.f)
}

func (g *cpyGen) genVar(v Var) {

	id := g.pkg.Name() + "_" + v.Name()
	doc := v.doc
	{
		res := []*Var{newVar(g.pkg, v.GoType(), "ret", v.Name(), doc)}
		sig := newSignature(g.pkg, nil, nil, res)
		fget := Func{
			pkg:  g.pkg,
			sig:  sig,
			typ:  nil,
			name: v.Name(),
			id:   id + "_get",
			doc:  "returns " + g.pkg.Name() + "." + v.Name(),
			ret:  v.GoType(),
			err:  false,
		}
		g.genFunc(fget)
	}
	{
		params := []*Var{newVar(g.pkg, v.GoType(), "arg", v.Name(), doc)}
		sig := newSignature(g.pkg, nil, params, nil)
		fset := Func{
			pkg:  g.pkg,
			sig:  sig,
			typ:  nil,
			name: v.Name(),
			id:   id + "_set",
			doc:  "sets " + g.pkg.Name() + "." + v.Name(),
			ret:  nil,
			err:  false,
		}
		g.genFunc(fset)
	}
}

func (g *cpyGen) genType(sym *symbol) {
	if !sym.isType() {
		return
	}
	if sym.isStruct() || sym.isBasic() {
		return
	}

	pkgname := sym.goobj.Pkg().Name()

	g.decl.Printf("/* --- decls for type %s.%v --- */\n", pkgname, sym.goname)
	g.decl.Printf("typedef void* %s;\n\n", sym.cgoname)
	g.decl.Printf("/* type for type %s.%v\n", pkgname, sym.goname)
	g.decl.Printf(" */\ntypedef struct {\n")
	g.decl.Indent()
	g.decl.Printf("PyObject_HEAD\n")
	g.decl.Printf("%[1]s cgopy; /* unsafe.Pointer to %[2]s */\n",
		sym.cgoname,
		sym.id,
	)
	g.decl.Outdent()
	g.decl.Printf("} %s;\n", sym.cpyname)
	g.decl.Printf("\n\n")

	g.impl.Printf("\n\n/* --- impl for %s.%v */\n\n", pkgname, sym.goname)

	g.genTypeNew(sym)
	g.genTypeDealloc(sym)
	g.genTypeInit(sym)
	g.genTypeMembers(sym)
	g.genTypeMethods(sym)

	g.genTypeProtocols(sym)

	g.impl.Printf("static PyTypeObject %sType = {\n", sym.cpyname)
	g.impl.Indent()
	g.impl.Printf("PyObject_HEAD_INIT(NULL)\n")
	g.impl.Printf("0,\t/*ob_size*/\n")
	g.impl.Printf("\"%s.%s\",\t/*tp_name*/\n", pkgname, sym.goname)
	g.impl.Printf("sizeof(%s),\t/*tp_basicsize*/\n", sym.cpyname)
	g.impl.Printf("0,\t/*tp_itemsize*/\n")
	g.impl.Printf("(destructor)%s_dealloc,\t/*tp_dealloc*/\n", sym.cpyname)
	g.impl.Printf("0,\t/*tp_print*/\n")
	g.impl.Printf("0,\t/*tp_getattr*/\n")
	g.impl.Printf("0,\t/*tp_setattr*/\n")
	g.impl.Printf("0,\t/*tp_compare*/\n")
	g.impl.Printf("0,\t/*tp_repr*/\n")
	g.impl.Printf("0,\t/*tp_as_number*/\n")
	g.impl.Printf("0,\t/*tp_as_sequence*/\n")
	g.impl.Printf("0,\t/*tp_as_mapping*/\n")
	g.impl.Printf("0,\t/*tp_hash */\n")
	g.impl.Printf("0,\t/*tp_call*/\n")
	g.impl.Printf("cpy_func_%s_tp_str,\t/*tp_str*/\n", sym.id)
	g.impl.Printf("0,\t/*tp_getattro*/\n")
	g.impl.Printf("0,\t/*tp_setattro*/\n")
	g.impl.Printf("0,\t/*tp_as_buffer*/\n")
	g.impl.Printf("Py_TPFLAGS_DEFAULT,\t/*tp_flags*/\n")
	g.impl.Printf("%q,\t/* tp_doc */\n", sym.doc)
	g.impl.Printf("0,\t/* tp_traverse */\n")
	g.impl.Printf("0,\t/* tp_clear */\n")
	g.impl.Printf("0,\t/* tp_richcompare */\n")
	g.impl.Printf("0,\t/* tp_weaklistoffset */\n")
	g.impl.Printf("0,\t/* tp_iter */\n")
	g.impl.Printf("0,\t/* tp_iternext */\n")
	g.impl.Printf("%s_methods,             /* tp_methods */\n", sym.cpyname)
	g.impl.Printf("0,\t/* tp_members */\n")
	g.impl.Printf("%s_getsets,\t/* tp_getset */\n", sym.cpyname)
	g.impl.Printf("0,\t/* tp_base */\n")
	g.impl.Printf("0,\t/* tp_dict */\n")
	g.impl.Printf("0,\t/* tp_descr_get */\n")
	g.impl.Printf("0,\t/* tp_descr_set */\n")
	g.impl.Printf("0,\t/* tp_dictoffset */\n")
	g.impl.Printf("(initproc)%s_init,      /* tp_init */\n", sym.cpyname)
	g.impl.Printf("0,                         /* tp_alloc */\n")
	g.impl.Printf("cpy_func_%s_new,\t/* tp_new */\n", sym.id)
	g.impl.Outdent()
	g.impl.Printf("};\n\n")

	g.genTypeConverter(sym)
}

func (g *cpyGen) genTypeNew(sym *symbol) {
	pkgname := sym.goobj.Pkg().Name()

	g.decl.Printf("/* tp_new for %s.%v */\n", pkgname, sym.goname)
	g.decl.Printf(
		"static PyObject*\ncpy_func_%s_new(PyTypeObject *type, PyObject *args, PyObject *kwds);\n",
		sym.id,
	)

	g.impl.Printf("/* tp_new */\n")
	g.impl.Printf(
		"static PyObject*\ncpy_func_%s_new(PyTypeObject *type, PyObject *args, PyObject *kwds) {\n",
		sym.id,
	)
	g.impl.Indent()
	g.impl.Printf("%s *self;\n", sym.cpyname)
	g.impl.Printf("self = (%s *)type->tp_alloc(type, 0);\n", sym.cpyname)
	g.impl.Printf("self->cgopy = cgo_func_%s_new();\n", sym.id)
	g.impl.Printf("return (PyObject*)self;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genTypeDealloc(sym *symbol) {
	pkgname := sym.goobj.Pkg().Name()

	g.decl.Printf("/* tp_dealloc for %s.%v */\n", pkgname, sym.goname)
	g.decl.Printf("static void\n%[1]s_dealloc(%[1]s *self);\n",
		sym.cpyname,
	)

	g.impl.Printf("/* tp_dealloc for %s.%v */\n", pkgname, sym.goname)
	g.impl.Printf("static void\n%[1]s_dealloc(%[1]s *self) {\n",
		sym.cpyname,
	)
	g.impl.Indent()
	g.impl.Printf("cgopy_decref((%[1]s)(self->cgopy));\n", sym.cgoname)
	g.impl.Printf("self->ob_type->tp_free((PyObject*)self);\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genTypeInit(sym *symbol) {
	pkgname := sym.goobj.Pkg().Name()

	g.decl.Printf("/* tp_init for %s.%v */\n", pkgname, sym.goname)
	g.decl.Printf(
		"static int\n%[1]s_init(%[1]s *self, PyObject *args, PyObject *kwds);\n",
		sym.cpyname,
	)

	g.impl.Printf("/* tp_init */\n")
	g.impl.Printf(
		"static int\n%[1]s_init(%[1]s *self, PyObject *args, PyObject *kwds) {\n",
		sym.cpyname,
	)
	g.impl.Indent()

	g.impl.Printf("return 0;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genTypeMembers(sym *symbol) {
	pkgname := sym.goobj.Pkg().Name()
	//g.decl.Printf("/* tp_getset for %s.%v */\n", pkgname, sym.goname)
	g.impl.Printf("/* tp_getset for %s.%v */\n", pkgname, sym.goname)
	g.impl.Printf("static PyGetSetDef %s_getsets[] = {\n", sym.cpyname)
	g.impl.Indent()
	g.impl.Printf("{NULL} /* Sentinel */\n")
	g.impl.Outdent()
	g.impl.Printf("};\n\n")
}

func (g *cpyGen) genTypeMethods(sym *symbol) {

	//pkgname := sym.goobj.Pkg().Name()
	//g.decl.Printf("/* methods for %s.%s */\n\n", pkgname, sym.goname)

	g.impl.Printf("static PyMethodDef %s_methods[] = {\n", sym.cpyname)
	g.impl.Indent()
	g.impl.Printf("{NULL} /* sentinel */\n")
	g.impl.Outdent()
	g.impl.Printf("};\n\n")
}

func (g *cpyGen) genTypeProtocols(sym *symbol) {
	g.genTypeTPStr(sym)
}

func (g *cpyGen) genTypeTPStr(sym *symbol) {
	g.decl.Printf(
		"static PyObject*\ncpy_func_%s_tp_str(PyObject *self);\n",
		sym.id,
	)

	g.impl.Printf(
		"static PyObject*\ncpy_func_%s_tp_str(PyObject *self) {\n",
		sym.id,
	)

	g.impl.Indent()
	g.impl.Printf("return PyObject_Repr(self);\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")
}

func (g *cpyGen) genTypeConverter(sym *symbol) {
	g.decl.Printf("\n/* converters for %s - %s */\n",
		sym.id,
		sym.goname,
	)
	g.decl.Printf("static int\n")
	g.decl.Printf("cgopy_cnv_py2c_%[1]s(PyObject *o, %[2]s *addr);\n",
		sym.id,
		sym.cgoname,
	)
	g.decl.Printf("static PyObject*\n")
	g.decl.Printf("cgopy_cnv_c2py_%[1]s(%[2]s *addr);\n\n",
		sym.id,
		sym.cgoname,
	)

	g.impl.Printf("static int\n")
	g.impl.Printf("cgopy_cnv_py2c_%[1]s(PyObject *o, %[2]s *addr) {\n",
		sym.id,
		sym.cgoname,
	)
	g.impl.Indent()
	g.impl.Printf("%s *self = NULL;\n", sym.cpyname)
	g.impl.Printf("self = (%s *)o;\n", sym.cpyname)
	g.impl.Printf("*addr = self->cgopy;\n")
	g.impl.Printf("return 1;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")

	g.impl.Printf("static PyObject*\n")
	g.impl.Printf("cgopy_cnv_c2py_%[1]s(%[2]s *addr) {\n", sym.id, sym.cgoname)
	g.impl.Indent()
	g.impl.Printf("PyObject *o = cpy_func_%[1]s_new(&%[2]sType, 0, 0);\n",
		sym.id,
		sym.cpyname,
	)
	g.impl.Printf("if (o == NULL) {\n")
	g.impl.Indent()
	g.impl.Printf("return NULL;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n")
	g.impl.Printf("((%[1]s*)o)->cgopy = *addr;\n", sym.cpyname)
	g.impl.Printf("return o;\n")
	g.impl.Outdent()
	g.impl.Printf("}\n\n")

}

func (g *cpyGen) genPreamble() {
	n := g.pkg.pkg.Name()
	g.decl.Printf(cPreamble, n, g.pkg.pkg.Path(), filepath.Base(n))
}
