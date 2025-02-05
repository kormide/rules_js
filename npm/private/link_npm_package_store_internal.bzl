"link_npm_package_store_internal rule"

load("@bazel_skylib//lib:dicts.bzl", "dicts")
load(":link_npm_package.bzl", _link_npm_package_store_lib = "link_npm_package_store_lib")
load(":npm_package.bzl", "NpmPackageInfo")

_INTERNAL_ATTRS_STORE = dicts.add(_link_npm_package_store_lib.attrs, {
    "src": attr.label(
        doc = """A npm_package target or or any other target that provides a NpmPackageInfo.

        Can be left unspecified to allow for link_npm_package "reference" targets. `link_npm_package`
        targets without a `src` are used internally by `npm_import` to create "reference"
        `link_npm_package` targets in order to break circular dependencies between 3rd party npm
        dependencies. This pattern is not recommended outside of `npm_import` as it adds
        complication. Outside our `npm_import` you should structure you `link_npm_package` targets in
        a DAG (without cycles).
        """,
        providers = [NpmPackageInfo],
    ),
    "package": attr.string(
        doc = """The package name to link to.
        
        Takes precendance over the package name in the NpmPackageInfo src.""",
        mandatory = True,
    ),
    "version": attr.string(
        doc = """The package version to link to.
        
        Takes precendance over the package version in the NpmPackageInfo src.""",
        mandatory = True,
    ),
})

link_npm_package_store_internal = rule(
    implementation = _link_npm_package_store_lib.implementation,
    attrs = _INTERNAL_ATTRS_STORE,
    provides = _link_npm_package_store_lib.provides,
)
