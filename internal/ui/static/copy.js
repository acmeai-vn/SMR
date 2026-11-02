(function () {
  document.querySelectorAll("[data-copy]").forEach(function (button) {
    var label = button.textContent;
    button.addEventListener("click", function () {
      var value = button.getAttribute("data-copy");
      var copied = button.getAttribute("data-copied");
      var done = function () {
        button.textContent = copied;
        setTimeout(function () { button.textContent = label; }, 2000);
      };
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(value).then(done, done);
        return;
      }
      var input = document.createElement("textarea");
      input.value = value;
      input.setAttribute("readonly", "");
      document.body.appendChild(input);
      input.select();
      try { document.execCommand("copy"); } catch (err) {}
      document.body.removeChild(input);
      done();
    });
  });
})();
