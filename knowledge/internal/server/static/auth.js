// Shared by login.html and signup.html — the two forms are identical except
// for which endpoint they post to and what to say on failure.
function wireAuthForm(endpoint, failureMessage) {
  document.getElementById("form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const errorEl = document.getElementById("error");
    errorEl.textContent = "";
    try {
      const response = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          email: document.getElementById("email").value,
          password: document.getElementById("password").value,
        }),
      });
      if (!response.ok) {
        const body = await response.json().catch(() => ({}));
        throw new Error(body.error?.message || failureMessage);
      }
      location.href = "/";
    } catch (error) {
      errorEl.textContent = error.message;
    }
  });
}
