class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.4.2"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.2/reminal_1.4.2_darwin_arm64.tar.gz"
      sha256 "332e1372cd278a72af202d3f90ea06f8288c2ebf97a7924c72f0b41c6abccbd6"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.2/reminal_1.4.2_darwin_amd64.tar.gz"
      sha256 "aa82b6dbcbd34811e44e1335acc6f8af8ef79617e83b3e6342648b97d0e09f4a"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.2/reminal_1.4.2_linux_arm64.tar.gz"
      sha256 "2b4a26f0413e611cbe276fe7d6e74765b731955ada6f28336f0b1ad780350eec"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.4.2/reminal_1.4.2_linux_amd64.tar.gz"
      sha256 "4a83aa5775d210e2e16e004622bc1f0d7208c8615071872205f18eb0a3a13ba5"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
