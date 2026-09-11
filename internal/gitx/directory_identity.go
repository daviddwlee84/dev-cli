package gitx

// DirectoryIdentity identifies an existing physical directory using the native
// filesystem identity. It rejects symlink/reparse directories, including a
// replacement clone at the same spelling used by an earlier guarded plan.
func DirectoryIdentity(path string) (string, error) { return submoduleDirectoryIdentity(path) }
