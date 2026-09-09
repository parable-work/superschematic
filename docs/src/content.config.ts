import { defineCollection } from 'astro:content';
import { docsLoader, i18nLoader } from '@astrojs/starlight/loaders';
import { docsSchema, i18nSchema } from '@astrojs/starlight/schema';

export const collections = {
  docs: defineCollection({ loader: docsLoader(), schema: docsSchema() }),
  // The site is English only. src/content/i18n/en.json is an empty override
  // set; declaring the collection with that one entry keeps the build free of
  // the "collection i18n does not exist or is empty" warning.
  i18n: defineCollection({ loader: i18nLoader(), schema: i18nSchema() }),
};
