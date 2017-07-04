function FormatEntryTime(entry) {
	if (!entry.Time) {
		return "-"
	}
	// TODO(adg): better date formatting.
	return (new Date(entry.Time*1000)).toLocaleString()
}

function FormatEntrySize(entry) {
	var size = 0;
	if (entry.Blocks) {
		for (var j=0; j<entry.Blocks.length; j++) {
			var block = entry.Blocks[j];
			size += block.Size;
		}
	} else {
		return "-";
	}
	return ""+size;
}

function FormatEntryAttr(entry) {
	var a = entry.Attr;
	var isDir = a & 1;
	var isLink = a & 2;
	var isIncomplete = a & 4;

	var s = "";
	if (isDir) {
		s = "Directory"
	}
	if (isLink) {
		s = "Link"
	}
	if (isIncomplete) {
		if (s != "") {
			s += ", ";
		}
		s += "Incomplete"
	}
	if (s == "") {
		s = "None"
	}
	return s
}

function Inspector() {
	var inspector = {
	};

	inspector.element = $(".up-template.up-inspector").clone().removeClass("up-template").hide();

	inspector.inspect = function(entry) {
		var el = inspector.element.show();
		console.log(entry);
		el.find(".up-entry-name").text(entry.Name);
		el.find(".up-entry-size").text(FormatEntrySize(entry));
		el.find(".up-entry-time").text(FormatEntryTime(entry));
		el.find(".up-entry-attr").text(FormatEntryAttr(entry));
		el.find(".up-entry-writer").text(entry.Writer);
	}

	return inspector;
}

function Browser(inspector) {
	var browser = {
		path: "",
		entries: [],
	};

	browser.element = $(".up-template.up-browser").clone().removeClass("up-template");

	browser.navigate = function(path) {
		browser.path = path;
		browser.drawBreadcrumbs();
		browser.drawLoading();
		browser.refresh();
	}
	var onClickNav = function() {
		var p = $(this).data("up-path");
		browser.navigate(p);
	};

	browser.refresh = function() {
		$.ajax("/", {
			data: {
				method: "list",
				path: browser.path,
			},
			dataType: "json",
			success: function(data) {
				if (!data.Entries) {
					browser.entries = [];
					browser.reportError(data.Error);
					return;
				}
				browser.entries = data.Entries;
				browser.drawEntries();
			},
			error: function(err) {
				browser.reportError(err);
			}
		});
	};

	browser.drawBreadcrumbs = function() {
		var parent = browser.element.find(".up-breadcrumb");
		parent.empty();

		var path = "";
		var tail = browser.path;
		while (tail.length > 0) {
			var name = tail;
			var i = name.indexOf("/");
			if (i > -1) {
				name = name.slice(0, i);
				tail = tail.slice(i+1);
			} else {
				tail = "";
			}
			path = path + name + "/"

			var el = $("<li>").text(name);
			if (tail == "") {
				el.addClass("active");
			} else {
				el.addClass("up-clickable");
				el.data("up-path", path.slice(0,-1));
				el.click(onClickNav);
			}
			parent.append(el);
		}
	};

	browser.drawLoading = function() {
		browser.element.find(".up-loading").show();
		browser.element.find(".up-error").hide();
		browser.element.find(".up-entries").hide();
	};

	browser.drawEntries = function() {
		browser.element.find(".up-loading").hide();
		browser.element.find(".up-error").hide();
		browser.element.find(".up-entries").show();

		var tmpl = browser.element.find(".up-template.up-entry");
		var parent = tmpl.parent();
		parent.children().filter(".up-entry").not(tmpl).remove();
		for (var i=0; i<browser.entries.length; i++) {
			var entry = browser.entries[i];
			var el = tmpl.clone().removeClass("up-template");

			var isDir = entry.Attr & 1;

			var name = entry.Name;
			name = name.slice(name.lastIndexOf("/")+1);
			var nameEl = el.find(".up-entry-name").text(name);
			if (isDir) {
				nameEl.addClass("up-clickable");
				nameEl.data("up-path", entry.Name);
				nameEl.click(onClickNav);
			}

			if (isDir) {
				el.find(".up-entry-size").text("-");
			} else{
				el.find(".up-entry-size").text(FormatEntrySize(entry));
			}

			el.find(".up-entry-time").text(FormatEntryTime(entry));

			var inspectEl = el.find(".up-entry-inspect");
			inspectEl.data("up-entry", entry);
			inspectEl.click(function() {
				inspector.inspect($(this).data("up-entry"));
			});

			parent.append(el);
		}
	};

	browser.reportError = function(err) {
		browser.element.find(".up-loading").hide();
		var el = browser.element.find(".up-error").show();
		el.text(err);
	};

	return browser;
}

var inspector = new Inspector();
var browser = new Browser(inspector);
$("body .container .row").append(browser.element).append(inspector.element);
browser.navigate("nf@wh3rd.net");
