// The notes-web site. superschematic scaffolded this file once; it is yours
// to change. loadApis() reads the address of each API the site calls from
// the config each environment serves at /__superschematic/config.json.
import { loadApis } from '../config.generated';

const app = document.getElementById('app');

loadApis().then(
  apis => {
    if (!app) return;
    app.replaceChildren(
      ...Object.values(apis).map(api => {
        const item = document.createElement('p');
        item.textContent = `${api.service}: ${api.baseUrl}`;
        return item;
      })
    );
  },
  (error: unknown) => {
    if (app) app.textContent = error instanceof Error ? error.message : String(error);
  }
);
