// Copyright 2017 The Upspin Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// FormatEntryTime returns the Time for the given DirEntry as a string.
function FormatEntryTime(entry) {
	if (!entry.Time) {
		return "-";
	}
	// TODO(adg): better date formatting.
	return (new Date(entry.Time*1000)).toLocaleString();
}

// FormatEntrySize returns the computed size of the given entry as a string.
function FormatEntrySize(entry) {
	if (!entry.Blocks) {
		return "-";
	}
	var size = 0;
	for (var j=0; j<entry.Blocks.length; j++) {
		size += entry.Blocks[j].Size;
	}
	return ""+size;
}

// FormatEntryAttr returns the Attributes for the given entry as a string.
function FormatEntryAttr(entry) {
	var a = entry.Attr;
	var isDir = a & 1;
	var isLink = a & 2;
	var isIncomplete = a & 4;

	var s = "";
	if (isDir) {
		s = "Directory";
	}
	if (isLink) {
		s = "Link";
	}
	if (isIncomplete) {
		if (s != "") {
			s += ", ";
		}
		s += "Incomplete";
	}
	if (s == "") {
		s = "None";
	}
	return s;
}

// Inspector displays a modal containing the details of the given entity.
function Inspect(entry) {
	var el = $("body > .up-inspector").modal("show");
	el.find(".up-entry-name").text(entry.Name);
	el.find(".up-entry-size").text(FormatEntrySize(entry));
	el.find(".up-entry-time").text(FormatEntryTime(entry));
	el.find(".up-entry-attr").text(FormatEntryAttr(entry));
	el.find(".up-entry-writer").text(entry.Writer);
}

// Confirm displays a modal that prompts the user to confirm the copy or delete
// of the given paths. If action is "copy", dest should be the copy destination.
// The callback argument is a niladic function that performs the action.
function Confirm(action, paths, dest, callback) {
	var el = $("body > .up-confirm");

	var button = el.find(".up-confirm-button");
	if (action == "delete") {
		button.addClass("btn-danger");
	} else {
		button.removeClass("btn-danger");
	}
	button.off("click").click(function() {
		el.modal("hide");
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

	el.modal("show");
}

// Mkdir displays a modal that prompts the user for a directory to create.
// The basePath is the path to pre-fill in the input box.
// The mkdir argument is a function that creates a directory and takes
// the path name as its single argument.
function Mkdir(basePath, mkdir) {
	var el = $("body > .up-mkdir");
	var input = el.find(".up-path").val(basePath);

	el.find(".up-mkdir-button").off("click").click(function() {
		el.modal("hide");
		mkdir(input.val());
	});

	el.modal("show").on("shown.bs.modal", function() {
		input.focus();
	});
}

// Browser instantiates an Upspin tree browser and appends it to parentEl.
function Browser(parentEl, page) {
	var browser = {
		path: "",
		entries: [],
		navigate: navigate,
		refresh: refresh,
		reportError: reportError
	};

	var el = $(".up-template.up-browser").clone().removeClass("up-template");
	el.appendTo(parentEl);

	function navigate(path) {
		browser.path = path;
		drawPath();
		drawLoading();
		page.list(path, function(entries) {
			drawEntries(entries);
		}, function(error) {
			reportError(error);
		});
	}

	function refresh() {
		navigate(browser.path);
	}

	function reportError(err) {
		loadingEl.hide();
		errorEl.show().text(err);
	}

	el.find(".up-delete").click(function() {
		var paths = checkedPaths();
		if (paths.length == 0) {
			return;
		}
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
		if (paths.length == 0) {
			return;
		}
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
		Mkdir(browser.path+"/", function(path) {
			page.mkdir(path, function() {
				refresh();
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
		navigate(p.slice(0, i));
	});

	var pathEl = el.find(".up-path").change(function() {
		navigate($(this).val());
	});

	function drawPath() {
		var p = browser.path;
		pathEl.val(p);

		var i = p.indexOf("/")
		parentEl.prop("disabled", atRoot());
	}

	var loadingEl = el.find(".up-loading"),
		errorEl = el.find(".up-error"),
		entriesEl = el.find(".up-entries");

	function drawLoading() {
		loadingEl.show();
		errorEl.hide();
		entriesEl.hide();
	}

	function drawEntries(entries) {
		loadingEl.hide();
		errorEl.hide();
		entriesEl.show();

		el.find(".up-select-all").prop("checked", false);

		var tmpl = el.find(".up-template.up-entry");
		var parent = tmpl.parent();
		parent.children().filter(".up-entry").not(tmpl).remove();
		for (var i=0; i<entries.length; i++) {
			var entry = entries[i];
			var entryEl = tmpl.clone().removeClass("up-template");
			entryEl.data("up-entry", entry);

			var isDir = entry.Attr & 1;
			var isLink = entry.Attr & 2;

			var glyph = "file";
			if (isDir) {
				glyph = "folder-close";
			} else if (isLink) {
				glyph = "share-alt";
			}
			entryEl.find(".up-entry-icon").addClass("glyphicon-"+glyph);

			var name = entry.Name;
			var shortName = name.slice(name.lastIndexOf("/")+1);
			var nameEl = entryEl.find(".up-entry-name");
			if (isDir) {
				nameEl.text(shortName);
				nameEl.addClass("up-clickable");
				nameEl.data("up-path", name);
				nameEl.click(function(event) {
					var p = $(this).data("up-path");
					navigate(p);
				});
			} else {
				var nameLink = $("<a>").text(shortName).attr("href", "/" + name).attr("target", "_blank");
				nameEl.append(nameLink);
			}

			var sizeEl = entryEl.find(".up-entry-size");
			if (isDir) {
				sizeEl.text("-");
			} else{
				sizeEl.text(FormatEntrySize(entry));
			}

			entryEl.find(".up-entry-time").text(FormatEntryTime(entry));

			var inspectEl = entryEl.find(".up-entry-inspect");
			inspectEl.data("up-entry", entry);
			inspectEl.click(function() {
				Inspect($(this).closest(".up-entry").data("up-entry"));
			});

			parent.append(entryEl);
		}
		var emptyEl = parent.find(".up-empty");
		if (entries.length == 0) {
			emptyEl.show();
		} else {
			emptyEl.hide();
		}
	}

	return browser;
}

// Startup manages the signup process and fetches the name of the logged-in
// user and the XSRF token for making subsequent requests.
function Startup(xhr, doneCallback) {
	var loadingEl = $("body > .up-loading").modal("show");
	loadingEl.find(".up-error").hide();

	var signupEl = $("body > .up-signup");
	signupEl.find("button").click(function() {
		// TODO: validate input before sending it to the server.
		action({
			action: "signup",
			username: $("#signupUserName").val(),
		});
	});

	var secretseedEl = $("body > .up-secretseed");
	secretseedEl.find("button").click(function() {
		action();
	});

	var verifyEl = $("body > .up-verify");
	verifyEl.find("button.up-resend").click(function() {
		action({action: "register"});
	});
	verifyEl.find("button.up-proceed").click(function() {
		action();
	});

	var serverSelectEl = $("body > .up-serverSelect");
	serverSelectEl.find("button").click(function() {
		switch (true) {
		case $("#serverSelectExisting").is(":checked"):
			show({Step: "serverExisting"});
			break;
		case $("#serverSelectGCP").is(":checked"):
			show({Step: "serverGCP"});
			break;
		case $("#serverSelectNone").is(":checked"):
			action({action: "specifyNoEndpoints"});
			break;
		}
	});
	var serverExistingEl = $("body > .up-serverExisting");
	serverExistingEl.find("button").click(function() {
		action({
			action: "specifyEndpoints",
			dirServer: $("#serverExistingDirServer").val(),
			storeServer: $("#serverExistingStoreServer").val()
		});
	});
	var serverGCPEl = $("body > .up-serverGCP");
	serverGCPEl.find("button").click(function() {
		var fileEl = $("#serverGCPKeyFile");
		if (fileEl[0].files.length != 1) {
			error("You must provide a JSON Private Key file.");
			return;
		}
		var r = new FileReader();
		r.onerror = function() {
			error("An error occurred uploading the file.");
		};
		r.onload = function(state) {
			action({
				action: "specifyGCP",
				privateKeyData: r.result
			});
		};
		r.readAsText(fileEl[0].files[0]);
	});
	var gcpDetailsEl = $("body > .up-gcpDetails");
	gcpDetailsEl.find("button").click(function() {
		action({
			action: "createGCP",
			bucketName: $("#gcpDetailsBucketName").val()
		});
	});
	var serverHostNameEl = $("body > .up-serverHostName");
	serverHostNameEl.find("button").click(function() {
		action({
			action: "configureServerHostName",
			hostName: $("#serverHostName").val()
		});
	});
	var serverUserNameEl = $("body > .up-serverUserName");
	serverUserNameEl.find("button").click(function() {
		action({
			action: "configureServerUserName",
			userNameSuffix: $("#serverUserNameSuffix").val()
		});
	});
	var serverSecretseedEl = $("body > .up-serverSecretseed");
	serverSecretseedEl.find("button").click(function() {
		show({Step: "serverHostName"});
	});
	var serverWritersEl = $("body > .up-serverWriters");
	serverWritersEl.find("button").click(function() {
		action({
			action: "configureServer",
			writers: $("#serverWriters").val()
		});
	});

	var lastStep = "loading";
	var lastEl = loadingEl;
	function success(resp) {
		if (!resp.Startup) {
			// The startup process is complete.
			if (lastEl) {
				lastEl.modal("hide");
			}
			doneCallback(resp);
			return;
		}
		show(resp.Startup);
	}
	function show(data) {
		// If we've moved onto another step, hide the previous one.
		if (lastEl && data.Step != lastStep) {
			lastEl.modal("hide");
		}

		// Set lastEl and lastStep and do step-specific setup.
		switch (data.Step) {
		case "signup":
			lastEl = signupEl;
			break;
		case "secretseed":
			$("#secretseedKeyDir").text(data.KeyDir);
			$("#secretseedSecretSeed").text(data.SecretSeed);
			lastEl = secretseedEl;
			break;
		case "verify":
			verifyEl.find(".up-username").text(data.UserName);
			lastEl = verifyEl;
			break;
		case "serverSelect":
			lastEl = serverSelectEl;
			break;
		case "serverExisting":
			lastEl = serverExistingEl;
			break;
		case "serverGCP":
			lastEl = serverGCPEl;
			break;
		case "gcpDetails":
			$("#gcpDetailsBucketName").val(data.BucketName);
			lastEl = gcpDetailsEl;
			break;
		case "serverUserName":
			$("#serverUserNamePrefix").text(data.UserNamePrefix);
			$("#serverUserNameSuffix").val(data.UserNameSuffix);
			$("#serverUserNameDomain").text(data.UserNameDomain);
			lastEl = serverUserNameEl;
			break;
		case "serverSecretseed":
			$("#serverSecretseedKeyDir").text(data.KeyDir);
			$("#serverSecretseedSecretSeed").text(data.SecretSeed);
			lastEl = serverSecretseedEl;
			break;
		case "serverHostName":
			serverHostNameEl.find(".up-ipAddr").text(data.IPAddr);
			lastEl = serverHostNameEl;
			break;
		case "serverWriters":
			serverWritersEl.find("#serverWriters").val(data.Writers.join("\n"));
			lastEl = serverWritersEl;
			break;
		}
		lastStep = data.Step;

		// Re-enable buttons, hide old errors, show the dialog.
		lastEl.find("button, input").prop("disabled", false);
		lastEl.find(".up-error").hide();
		lastEl.modal("show");
	}
	function error(err) {
		if (lastEl) {
			// Show the error, re-enable buttons.
			lastEl.find(".up-error").show().find(".up-error-msg").text(err);
			lastEl.find("button, input").prop("disabled", false);
		} else {
			alert(err)
			// TODO(adg): display the initial error in a more friendly way.
		}
	}
	function action(data) {
		if (lastEl) {
			// Disable buttons, hide old errors.
			lastEl.find("button, input").prop("disabled", true);
			lastEl.find(".up-error").hide();
		}
		xhr(data, success, error);
	}

	action(); // Kick things off.
}

function Page() {
	var page = {
		username: "",
		token: ""
	};

	// errorHandler returns an XHR error callback that invokes the given
	// browser error callback with the human-readable error string.
	function errorHandler(callback) {
		return function(jqXHR, textStatus, errorThrown) {
			console.log(textStatus, errorThrown);
			if (errorThrown) {
				callback(errorThrown);
				return;
			}
			callback(textStatus);
		}
	}

	function list(path, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: {
				token: page.token,
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
			error: errorHandler(error)
		});
	}

	function rm(paths, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: {
				token: page.token,
				method: "rm",
				paths: paths
			},
			dataType: "json",
			success: function(data) {
				if (data.Error) {
					error(data.Error);
					return;
				}
				success();
			},
			error: errorHandler(error)
		});
	}

	function copy(paths, dest, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: {
				token: page.token,
				method: "copy",
				paths: paths,
				dest: dest
			},
			dataType: "json",
			success: function(data) {
				if (data.Error) {
					error(data.Error);
					return;
				}
				success();
			},
			error: errorHandler(error)
		});
	}

	function mkdir(path, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: {
				token: page.token,
				method: "mkdir",
				path: path
			},
			dataType: "json",
			success: function(data) {
				if (data.Error) {
					error(data.Error);
					return;
				}
				success();
			},
			error: errorHandler(error)
		});
	}

	function startup(data, success, error) {
		$.ajax("/_upspin", {
			method: "POST",
			data: $.extend({method: "startup"}, data),
			dataType: "json",
			success: function(data) {
				if (data.Error) {
					error(data.Error);
					return;
				}
				success(data);
			},
			error: errorHandler(error)
		});
	}

	function startBrowsers() {
		var browser1, browser2;
		var parentEl = $(".up-browser-parent");
		var methods = {
			rm: rm,
			copy: copy,
			list: list,
			mkdir: mkdir,
		}
		browser1 = new Browser(parentEl, $.extend({
			copyDestination: function() { return browser2.path },
			refreshDestination: function() { browser2.refresh(); }
		}, methods));
		browser2 = new Browser(parentEl, $.extend({
			copyDestination: function() { return browser1.path },
			refreshDestination: function() { browser1.refresh(); }
		}, methods));
		browser1.navigate(page.username);
		browser2.navigate("augie@upspin.io");
	}

	// Begin the Startup sequence.
	Startup(startup, function(data) {
		// When startup is complete note the user name and token and
		// launch the browsers.
		page.username = data.UserName;
		page.token = data.Token;
		$("#headerUsername").text(page.username);
		startBrowsers();
	});
}

// Start everything.
new Page();
