class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.6.1"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.6.1/reminal_1.6.1_darwin_arm64.tar.gz"
      sha256 "fedb51abdb9627c9c3d0e5db200b8d2c4e46f589d8e7021abe8f0b36acbee12a"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.6.1/reminal_1.6.1_darwin_amd64.tar.gz"
      sha256 "b58695674510da81713433331c0790caa12ff54c9c7240de87bdc152c0851257"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.6.1/reminal_1.6.1_linux_arm64.tar.gz"
      sha256 "aaa39cac7477404044aca59d8bd1482d87f0004f91416d4b4c904a6c30a7542f"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.6.1/reminal_1.6.1_linux_amd64.tar.gz"
      sha256 "8c57ef5c796443b26b0957e876195efda45c98d94ece8a227f59b9f121d21268"
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
