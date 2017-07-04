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

	inspector.element = $(".up-inspector");

	inspector.inspect = function(entry) {
		var el = inspector.element.modal('show');
		el.find(".up-entry-name").text(entry.Name);
		el.find(".up-entry-size").text(FormatEntrySize(entry));
		el.find(".up-entry-time").text(FormatEntryTime(entry));
		el.find(".up-entry-attr").text(FormatEntryAttr(entry));
		el.find(".up-entry-writer").text(entry.Writer);
	}

	return inspector;
}

function Confirm(action, paths, dest, callback) {
	var el = $("body > .up-confirm");

	var button = el.find(".up-confirm-button");
	if (action == "delete") {
		button.addClass("btn-danger");
	} else {
		button.removeClass("btn-danger");
	}
	button.click(function() {
		el.modal('hide');
		callback();
	});

	el.find(".up-action").text(action);

	var pathsEl = el.find(".up-paths").empty();
	for (var i=0; i<paths.length; i++) {
		pathsEl.append($("<li>").text(paths[i]));
	}

	if (dest) {
		el.find(".up-dest-message").show();
		el.find(".up-dest").text(dest);
	} else {
		el.find(".up-dest-message").hide();
	}

	$('body').prepend(el);
	el.modal('show');
}

function Open(defaultPath, navigate) {
	var el = $("body > .up-open");

	var text = el.find(".up-path").val(defaultPath);

	el.find(".up-open-button").click(function() {
		el.modal('hide');
		el.on('hidden', function() {
			el.remove();
		});
		navigate(text.val());
	});

	$('body').prepend(el);
	el.modal('show');
}

function Browser(page) {
	var browser = {
		path: "",
		entries: [],
	};

	browser.element = $(".up-template.up-browser").clone().removeClass("up-template");

	browser.element.find(".up-select-all").on("change", function() {
		var checked = $(this).is(":checked");
		browser.element.find(".up-entry").not(".up-template").find(".up-entry-select").each(function() {
			$(this).prop("checked", checked);
		});
	});

	function checkedPaths() {
		var paths = [];
		browser.element.find(".up-entry").not(".up-template").each(function() {
			var checked = $(this).find(".up-entry-select").is(":checked");
			if (checked) {
				paths.push($(this).data("up-entry").Name);
			}
		});
		return paths;
	}

	browser.element.find(".up-delete").click(function() {
		var paths = checkedPaths();
		if (paths.length == 0) return;
		Confirm("delete", paths, null, function() {
			page.rm(paths);
		});
	});

	browser.element.find(".up-copy").click(function() {
		var paths = checkedPaths();
		if (paths.length == 0) return;
		var dest = page.copyDestination();
		Confirm("copy", paths, dest, function() {
			page.copy(paths, dest);
		});
	});

	browser.element.find(".up-open").click(function() {
		Open(browser.path, browser.navigate);
	});

	browser.navigate = function(path) {
		browser.path = path;
		browser.drawBreadcrumbs();
		browser.drawLoading();
		page.list(path, function(entries) {
			browser.entries = entries;
			browser.drawEntries();
		}, function(error) {
			browser.reportError(error);
		});
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

	var loadingEl = browser.element.find(".up-loading"),
		errorEl = browser.element.find(".up-error"),
		entriesEl = browser.element.find(".up-entries");

	browser.drawLoading = function() {
		loadingEl.show();
		errorEl.hide();
		entriesEl.hide();
	};

	browser.drawEntries = function() {
		loadingEl.hide();
		errorEl.hide();
		entriesEl.show();

		var tmpl = browser.element.find(".up-template.up-entry");
		var parent = tmpl.parent();
		parent.children().filter(".up-entry").not(tmpl).remove();
		for (var i=0; i<browser.entries.length; i++) {
			var entry = browser.entries[i];
			var el = tmpl.clone().removeClass("up-template");
			el.data("up-entry", entry);

			var isDir = entry.Attr & 1;
			var isLink = entry.Attr & 2;

			var glyph = "file";
			if (isDir) {
				glyph = "folder-close";
			} else if (isLink) {
				glyph = "share-alt";
			}
			el.find(".up-entry-icon").addClass("glyphicon-"+glyph);

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
				page.inspect($(this).closest(".up-entry").data("up-entry"));
			});

			parent.append(el);
		}
	};

	browser.reportError = function(err) {
		loadingEl.hide();
		errorEl.show().text(err);
		entriesEl.hide();
	};

	return browser;
}

function Page() {
	var page = {};

	var inspector = new Inspector();
	$("body").prepend(inspector.element);

	var browser1, browser2;

	function list(path, success, error) {
		$.ajax("/", {
			data: {
				method: "list",
				path: path,
			},
			dataType: "json",
			success: function(data) {
				if (!data.Entries) {
					error(data.Error);
					return;
				}
				success(data.Entries);
			},
			error: error
		});
	}

	function rm(paths) {
		console.log("rm", paths);
	}

	function copy(paths, dest) {
		console.log("copy", paths, dest);
	}

	browser1 = new Browser({
		inspect: inspector.inspect,
		rm: rm,
		copy: copy,
		list: list,
		copyDestination: function() { return browser2.path }
	});
	browser2 = new Browser({
		inspect: inspector.inspect,
		rm: rm,
		copy: copy,
		list: list,
		copyDestination: function() { return browser1.path }
	});
	$(".up-browser-parent").append(browser1.element).append(browser2.element);

	browser1.navigate("nf@wh3rd.net");
	browser2.navigate("augie@upspin.io");

	// Populate the username in the header.
	$.ajax("/", {
		data: {
			method: "whoami",
		},
		dataType: "json",
		success: function(data) {
			page.username = data.UserName;
			$(".up-username").text(page.username);
		},
		error: function(err) {
			console.log(err);
		}
	});
}

new Page();
