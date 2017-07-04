function Browser(path) {
	var browser = {
		path: path,
		entries: [],
	};

	browser.element = $(".up-template.up-browser").clone().removeClass("up-template");

	browser.navigate = function(path) {
		browser.path = path;
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
				browser.entries = data.Entries;
				browser.drawBreadcrumbs();
				browser.drawEntries();
			},
			error: function(err) {
				console.log(err);
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

	browser.drawEntries = function() {
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

			var size = 0;
			if (entry.Blocks) {
				for (var j=0; j<entry.Blocks.length; j++) {
					var block = entry.Blocks[j];
					size += block.Size;
				}
			}
			if (isDir) {
				el.find(".up-entry-size").text("-");
			} else{
				el.find(".up-entry-size").text(size);
			}

			// TODO(adg): better date formatting.
			var d = new Date(entry.Time*1000);
			el.find(".up-entry-time").text(d.toLocaleString());

			parent.append(el);
		}
	};

	return browser;
}

var b = new Browser("nf@wh3rd.net");
b.refresh();
$("body").append(b.element);
