(() => {
  try {
    const themeMode = localStorage.getItem('materialmind.themeMode');
    document.documentElement.dataset.theme =
      themeMode === 'light' || themeMode === 'dark' ? themeMode : 'system';
  } catch {
    document.documentElement.dataset.theme = 'system';
  }
})();
