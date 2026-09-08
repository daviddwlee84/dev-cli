$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)
# The path is stdin data. No source text is constructed from a path.
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;

[ComImport, Guid("43826D1E-E718-42EE-BC55-A1E261C37BFE"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
public interface IShellItem {
    void BindToHandler(IntPtr pbc, ref Guid bhid, ref Guid riid, out IntPtr result);
    void GetParent(out IShellItem parent);
    void GetDisplayName(uint type, out IntPtr name);
    void GetAttributes(uint mask, out uint attributes);
    void Compare(IShellItem other, uint hint, out int order);
}

[ComImport, Guid("947AAB5F-0A5C-4C13-B4D6-4BF7836FC9F8"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
interface IFileOperation {
    void Advise(IFileOperationProgressSink sink, out uint cookie);
    void Unadvise(uint cookie);
    void SetOperationFlags(uint flags);
    void SetProgressMessage([MarshalAs(UnmanagedType.LPWStr)] string message);
    void SetProgressDialog(IntPtr dialog);
    void SetProperties(IntPtr properties);
    void SetOwnerWindow(IntPtr owner);
    void ApplyPropertiesToItem(IShellItem item);
    void ApplyPropertiesToItems(IntPtr items);
    void RenameItem(IShellItem item, [MarshalAs(UnmanagedType.LPWStr)] string name, IFileOperationProgressSink sink);
    void RenameItems(IntPtr items, [MarshalAs(UnmanagedType.LPWStr)] string name);
    void MoveItem(IShellItem item, IShellItem folder, [MarshalAs(UnmanagedType.LPWStr)] string name, IFileOperationProgressSink sink);
    void MoveItems(IntPtr items, IShellItem folder);
    void CopyItem(IShellItem item, IShellItem folder, [MarshalAs(UnmanagedType.LPWStr)] string name, IFileOperationProgressSink sink);
    void CopyItems(IntPtr items, IShellItem folder);
    void DeleteItem(IShellItem item, IFileOperationProgressSink sink);
    void DeleteItems(IntPtr items);
    void NewItem(IShellItem folder, uint attributes, [MarshalAs(UnmanagedType.LPWStr)] string name, [MarshalAs(UnmanagedType.LPWStr)] string template, IFileOperationProgressSink sink);
    void PerformOperations();
    void GetAnyOperationsAborted([MarshalAs(UnmanagedType.Bool)] out bool aborted);
}

[ComVisible(true), Guid("04B0F1A7-9490-44BC-96E1-4296A31252E2"), InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
public interface IFileOperationProgressSink {
    [PreserveSig] int StartOperations();
    [PreserveSig] int FinishOperations(int hr);
    [PreserveSig] int PreRenameItem(uint flags, IShellItem item, [MarshalAs(UnmanagedType.LPWStr)] string name);
    [PreserveSig] int PostRenameItem(uint flags, IShellItem item, [MarshalAs(UnmanagedType.LPWStr)] string name, int hr, IShellItem result);
    [PreserveSig] int PreMoveItem(uint flags, IShellItem item, IShellItem folder, [MarshalAs(UnmanagedType.LPWStr)] string name);
    [PreserveSig] int PostMoveItem(uint flags, IShellItem item, IShellItem folder, [MarshalAs(UnmanagedType.LPWStr)] string name, int hr, IShellItem result);
    [PreserveSig] int PreCopyItem(uint flags, IShellItem item, IShellItem folder, [MarshalAs(UnmanagedType.LPWStr)] string name);
    [PreserveSig] int PostCopyItem(uint flags, IShellItem item, IShellItem folder, [MarshalAs(UnmanagedType.LPWStr)] string name, int hr, IShellItem result);
    [PreserveSig] int PreDeleteItem(uint flags, IShellItem item);
    [PreserveSig] int PostDeleteItem(uint flags, IShellItem item, int hr, IShellItem result);
    [PreserveSig] int PreNewItem(uint flags, IShellItem folder, [MarshalAs(UnmanagedType.LPWStr)] string name);
    [PreserveSig] int PostNewItem(uint flags, IShellItem folder, [MarshalAs(UnmanagedType.LPWStr)] string name, [MarshalAs(UnmanagedType.LPWStr)] string template, uint attributes, int hr, IShellItem result);
    [PreserveSig] int UpdateProgress(uint total, uint completed);
    [PreserveSig] int ResetTimer();
    [PreserveSig] int PauseTimer();
    [PreserveSig] int ResumeTimer();
}

[ComVisible(true), ClassInterface(ClassInterfaceType.None)]
public class RecycleSink : IFileOperationProgressSink {
    internal bool Recycled;
    const int Abort = unchecked((int)0x80004004);
    public int PreDeleteItem(uint flags, IShellItem item) {
        // Never consent to the permanent-delete branch on non-recyclable
        // media. An error here cancels the complete IFileOperation.
        return (flags & 0x80) != 0 ? 0 : Abort;
    }
    public int PostDeleteItem(uint flags, IShellItem item, int hr, IShellItem result) {
        Recycled = hr >= 0 && (flags & 0x80) != 0 && result != null;
        return Recycled ? 0 : Abort;
    }
    public int StartOperations() { return 0; }
    public int FinishOperations(int hr) { return hr; }
    public int PreRenameItem(uint f, IShellItem i, string n) { return Abort; }
    public int PostRenameItem(uint f, IShellItem i, string n, int h, IShellItem r) { return Abort; }
    public int PreMoveItem(uint f, IShellItem i, IShellItem d, string n) { return Abort; }
    public int PostMoveItem(uint f, IShellItem i, IShellItem d, string n, int h, IShellItem r) { return Abort; }
    public int PreCopyItem(uint f, IShellItem i, IShellItem d, string n) { return Abort; }
    public int PostCopyItem(uint f, IShellItem i, IShellItem d, string n, int h, IShellItem r) { return Abort; }
    public int PreNewItem(uint f, IShellItem d, string n) { return Abort; }
    public int PostNewItem(uint f, IShellItem d, string n, string t, uint a, int h, IShellItem r) { return Abort; }
    public int UpdateProgress(uint t, uint c) { return 0; }
    public int ResetTimer() { return 0; }
    public int PauseTimer() { return 0; }
    public int ResumeTimer() { return 0; }
}

public static class DevRecycle {
    [DllImport("shell32.dll", CharSet=CharSet.Unicode, PreserveSig=false)]
    static extern void SHCreateItemFromParsingName(string path, IntPtr bind, ref Guid iid, out IShellItem item);
    public static void Run(string path) {
        Guid iid = typeof(IShellItem).GUID;
        IShellItem item;
        SHCreateItemFromParsingName(path, IntPtr.Zero, ref iid, out item);
        IFileOperation op = (IFileOperation)Activator.CreateInstance(Type.GetTypeFromCLSID(new Guid("3AD05575-8857-4850-9277-11B85BDB8E09")));
        try {
            // SILENT, NOERRORUI, WANTNUKEWARNING, RECYCLEONDELETE,
            // EARLYFAILURE and ADDUNDORECORD. No permanent-delete consent.
            op.SetOperationFlags(0x20184404);
            RecycleSink sink = new RecycleSink();
            op.DeleteItem(item, sink);
            op.PerformOperations();
            bool aborted;
            op.GetAnyOperationsAborted(out aborted);
            if (aborted || !sink.Recycled) throw new InvalidOperationException("No verified recycle operation; permanent deletion was not authorized.");
        } finally {
            Marshal.ReleaseComObject(op);
            Marshal.ReleaseComObject(item);
        }
    }
}
'@
[DevRecycle]::Run([Console]::In.ReadToEnd())
[Console]::Out.WriteLine('recycled')
