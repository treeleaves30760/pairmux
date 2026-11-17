// Wraps the theme's 404 content only to keep the page out of search indexes
// and give it a real title. In docs-only mode the docs root route renders
// this component (not @theme/NotFound) for unknown paths, including the
// generated 404.html, so this is the one place that covers both. GitHub Pages
// already answers unknown paths with HTTP 404; the noindex covers 404.html
// fetched directly, whose canonical would otherwise invite indexing.
import React, {type ReactNode} from 'react';
import Head from '@docusaurus/Head';
import {PageMetadata} from '@docusaurus/theme-common';
import Content from '@theme-original/NotFound/Content';
import type ContentType from '@theme/NotFound/Content';
import type {WrapperProps} from '@docusaurus/types';

type Props = WrapperProps<typeof ContentType>;

export default function ContentWrapper(props: Props): ReactNode {
  return (
    <>
      <PageMetadata title="Page Not Found" />
      <Head>
        <meta name="robots" content="noindex" />
      </Head>
      <Content {...props} />
    </>
  );
}
