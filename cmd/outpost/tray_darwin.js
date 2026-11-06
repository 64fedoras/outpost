// Outpost menu bar helper. Run by `outpost tray` on macOS as
//   osascript -l JavaScript tray_darwin.js
// The Go side writes one JSON state per line to stdin ({icon, tip, menu});
// menu clicks go back as "OUTPOST-CLICK:<id>" lines on stderr (console.log in
// osascript writes to stderr). When stdin closes, this process quits.
ObjC.import('Cocoa');

var statusItem, handler, stdinHandle, inbuf = '';

function apply(s) {
  var button = statusItem.button;
  if (s.icon) {
    var data = $.NSData.alloc.initWithBase64EncodedStringOptions(s.icon, 0);
    var img = $.NSImage.alloc.initWithData(data);
    img.setSize($.NSMakeSize(18, 18));
    button.image = img;
  }
  button.toolTip = s.tip || '';
  var menu = $.NSMenu.alloc.init;
  menu.autoenablesItems = false;
  (s.menu || []).forEach(function (e) {
    if (e.sep) { menu.addItem($.NSMenuItem.separatorItem); return; }
    var mi = $.NSMenuItem.alloc.initWithTitleActionKeyEquivalent(e.title || '', 'clicked:', '');
    mi.target = handler;
    mi.tag = e.id;
    mi.enabled = !e.disabled;
    mi.state = e.checked ? 1 : 0;
    if (e.tip) { mi.toolTip = e.tip; }
    menu.addItem(mi);
  });
  statusItem.menu = menu;
}

function onLine(line) {
  if (!line) { return; }
  try { apply(JSON.parse(line)); } catch (err) { console.log('OUTPOST-ERROR:' + err); }
}

ObjC.registerSubclass({
  name: 'OutpostHandler',
  superclass: 'NSObject',
  methods: {
    'clicked:': {
      types: ['void', ['id']],
      implementation: function (sender) { console.log('OUTPOST-CLICK:' + sender.tag); }
    },
    'gotData:': {
      types: ['void', ['id']],
      implementation: function (note) {
        var data = note.userInfo.objectForKey($.NSFileHandleNotificationDataItem);
        if (data.length === 0) { $.NSApplication.sharedApplication.terminate(null); return; }
        var str = $.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding);
        inbuf += ObjC.unwrap(str);
        var parts = inbuf.split('\n');
        inbuf = parts.pop();
        parts.forEach(onLine);
        stdinHandle.readInBackgroundAndNotify;
      }
    }
  }
});

var app = $.NSApplication.sharedApplication;
app.setActivationPolicy($.NSApplicationActivationPolicyAccessory);
handler = $.OutpostHandler.alloc.init;
statusItem = $.NSStatusBar.systemStatusBar.statusItemWithLength($.NSVariableStatusItemLength);
statusItem.button.title = '';
stdinHandle = $.NSFileHandle.fileHandleWithStandardInput;
$.NSNotificationCenter.defaultCenter.addObserverSelectorNameObject(handler, 'gotData:', $.NSFileHandleReadCompletionNotification, stdinHandle);
stdinHandle.readInBackgroundAndNotify;
console.log('OUTPOST-READY');
app.run;
