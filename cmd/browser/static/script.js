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
		element: $(".up-inspector"),
	};

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
	button.off('click').click(function() {
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

function MkDir(basePath, mkdir) {
	var el = $("body > .up-mkdir");

	var text = el.find(".up-path").val(basePath);

	el.find(".up-mkdir-button").off('click').click(function() {
		el.modal('hide');
		el.on('hidden', function() {
			el.remove();
		});
		mkdir(text.val());
	});

	$('body').prepend(el);
	el.modal('show');
}

function Browser(page) {
	var el = $(".up-template.up-browser").clone().removeClass("up-template");
	var browser = {
		path: "",
		entries: [],
		element: el,
		navigate: navigate,
		refresh: refresh,
		reportError: reportError,
	};

	function navigate(path) {
		browser.path = path;
		drawPath();
		drawLoading();
		page.list(path, function(entries) {
			drawEntries(entries)
		}, function(error) {
			reportError(error);
		});
	}

	function refresh() {
		navigate(browser.path);
	}

	el.find(".up-delete").click(function() {
		var paths = checkedPaths();
		if (paths.length == 0) return;
		Confirm("delete", paths, null, function() {
			page.rm(paths, function() {
				refresh();
			}, function(err) {
				reportError(err);
			});
		});
	});

	el.find(".up-copy").click(function() {
		var paths = checkedPaths();
		if (paths.length == 0) return;
		var dest = page.copyDestination();
		Confirm("copy", paths, dest, function() {
			page.copy(paths, dest, function() {
				page.refreshDestination();
			}, function(error) {
				reportError(error);
			});
		});
	});

	el.find(".up-refresh").click(function() {
		refresh();
	});

	el.find(".up-mkdir").click(function() {
		MkDir(browser.path+"/", function(path) {
			page.mkdir(path, function() {
				page.refreshDestination();
			}, function(error) {
				reportError(error);
			});
		});
	});

	el.find(".up-select-all").on("change", function() {
		var checked = $(this).is(":checked");
		el.find(".up-entry").not(".up-template").find(".up-entry-select").each(function() {
			$(this).prop("checked", checked);
		});
	});

	function checkedPaths() {
		var paths = [];
		el.find(".up-entry").not(".up-template").each(function() {
			var checked = $(this).find(".up-entry-select").is(":checked");
			if (checked) {
				paths.push($(this).data("up-entry").Name);
			}
		});
		return paths;
	}

	function atRoot() {
		var p = browser.path;
		var i = p.indexOf("/");
		return i == -1 || i == p.length-1;
	}

	var parentEl = el.find(".up-parent").click(function() {
		if (atRoot()) return;

		var p = browser.path;
		var i = p.lastIndexOf("/");
		navigate(p.slice(0, i))
	});

	var pathEl = el.find(".up-path").change(function() {
		navigate($(this).val());
	});

	function drawPath() {
		var p = browser.path;
		pathEl.val(p);

		var i = p.indexOf("/")
		parentEl.prop("disabled", atRoot());
	};

	var loadingEl = el.find(".up-loading"),
		errorEl = el.find(".up-error"),
		entriesEl = el.find(".up-entries");

	function drawLoading() {
		loadingEl.show();
		errorEl.hide();
		entriesEl.hide();
	};

	function drawEntries(entries) {
		loadingEl.hide();
		errorEl.hide();
		entriesEl.show();

		var tmpl = browser.element.find(".up-template.up-entry");
		var parent = tmpl.parent();
		parent.children().filter(".up-entry").not(tmpl).remove();
		for (var i=0; i<entries.length; i++) {
			var entry = entries[i];
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
			var nameLink = $("<a>").attr("href", "file://" + page.upspinfs() + "/" + name);
			nameLink.text(name.slice(name.lastIndexOf("/")+1));
			var nameEl = el.find(".up-entry-name").append(nameLink);
			if (isDir) {
				nameLink.click(function(event) {
					event.preventDefault();
				});
				nameEl.addClass("up-clickable");
				nameEl.data("up-path", name);
				nameEl.click(function(event) {
					var p = $(this).data("up-path");
					navigate(p);
				});
			}

			var sizeEl = el.find(".up-entry-size");
			if (isDir) {
				sizeEl.text("-");
			} else{
				sizeEl.text(FormatEntrySize(entry));
			}

			el.find(".up-entry-time").text(FormatEntryTime(entry));

			var inspectEl = el.find(".up-entry-inspect");
			inspectEl.data("up-entry", entry);
			inspectEl.click(function() {
				page.inspect($(this).closest(".up-entry").data("up-entry"));
			});

			parent.append(el);
		}
		var emptyEl = parent.find(".up-empty");
		if (entries.length == 0) {
			emptyEl.show();
		} else {
			emptyEl.hide();
		}
	};

	function reportError(err) {
		loadingEl.hide();
		errorEl.show().text(err);
	};

	return browser;
}

function Page() {
	var page = {
		upspinfs: "",
		username: ""
	};

	var inspector = new Inspector();
	$("body").prepend(inspector.element);

	function list(path, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: {
				method: "list",
				path: path,
			},
			dataType: "json",
			success: function(data) {
				if (data.Error) {
					error(data.Error);
					return;
				}
				success(data.Entries);
			},
			error: error
		});
	}

	function rm(paths, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: {
				method: "rm",
				paths: paths,
			},
			dataType: "json",
			success: function(data) {
				if (data.Error) {
					error(data.Error);
					return;
				}
				success();
			},
			error: error
		});
	}

	function copy(paths, dest, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: {
				method: "copy",
				paths: paths,
				dest: dest,
			},
			dataType: "json",
			success: function(data) {
				if (data.Error) {
					error(data.Error);
					return;
				}
				success();
			},
			error: error
		});
	}

	function mkdir(path, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: {
				method: "mkdir",
				path: path,
			},
			dataType: "json",
			success: function(data) {
				if (data.Error) {
					error(data.Error);
					return;
				}
				success();
			},
			error: error
		});
	}

	var browser1, browser2;
	var methods = {
		inspect: inspector.inspect,
		rm: rm,
		copy: copy,
		list: list,
		mkdir: mkdir,
		upspinfs: function() { return page.upspinfs }
	}
	browser1 = new Browser($.extend({
		copyDestination: function() { return browser2.path },
		refreshDestination: function() { browser2.refresh(); }
	}, methods));
	browser2 = new Browser($.extend({
		copyDestination: function() { return browser1.path },
		refreshDestination: function() { browser1.refresh(); }
	}, methods));
	$(".up-browser-parent").append(browser1.element).append(browser2.element);

	// Populate the username in the header.
	$.ajax("/_upspin", {
		method: "POST",
		data: {
			method: "whoami",
		},
		dataType: "json",
		success: function(data) {
			page.upspinfs = data.Upspinfs;
			page.username = data.UserName;

			$(".up-username").text(page.username);
			browser1.navigate(page.username);
			browser2.navigate("augie@upspin.io");
		},
		error: function(err) {
			browser1.reportError(err);
		}
	});
}

new Page();
